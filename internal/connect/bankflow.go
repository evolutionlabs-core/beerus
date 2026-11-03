package connect

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"slices"
)

func (a *App) beginBankAuth(w http.ResponseWriter, r *http.Request, ls *LinkSession) {
	b := a.cfg.Bank(ls.Institution)
	if b == nil || b.APIPort == 0 {
		a.failSession(w, r, ls, "institution_down")
		return
	}
	verifier, challenge := pkcePair()
	state := newSecret("bs_", 16)
	uri, err := a.bankPAR(b, ls, state, challenge)
	if err != nil {
		if !isDown(err) {
			a.log.Error("pushed authorisation request", "bank", b.ID, "err", err)
			a.failSession(w, r, ls, "server_error")
			return
		}
		a.failSession(w, r, ls, "institution_down")
		return
	}
	ls.BankState, ls.Verifier, ls.Status = state, verifier, "at_bank"
	a.dirty()
	q := url.Values{"client_id": {a.cfg.Connect.RegistryID}, "request_uri": {uri}}
	http.Redirect(w, r, b.URL()+"/authorize?"+q.Encode(), http.StatusSeeOther)
}

func (a *App) bankCallback(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ls := a.session(w, r)
	if ls == nil {
		return
	}
	q := r.URL.Query()
	if !a.live() || ls.BankState == "" || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(ls.BankState)) != 1 {
		a.message(w, http.StatusForbidden, "Request refused", "This sign-in does not match your session. Go back to the app and start again.")
		return
	}
	b := a.cfg.Bank(ls.Institution)
	verifier := ls.Verifier
	ls.BankState, ls.Verifier = "", ""
	a.dirty()
	if b == nil || q.Get("iss") != b.APIURL() {
		a.failSession(w, r, ls, "server_error")
		return
	}
	if e := q.Get("error"); e != "" {
		if e == "access_denied" {
			a.failSession(w, r, ls, e)
		} else {
			a.failSession(w, r, ls, "server_error")
		}
		return
	}
	vid := linkVaultPrefix + ls.ID
	tok, err := a.bankExchange(b, q.Get("code"), verifier)
	if err == nil {
		err = a.vaultPut(vid, tok)
	}
	var g bankGrant
	if err == nil {
		err = a.bankGet(b, vid, "/accounts", &g)
	}
	if err != nil {
		a.log.Error("bank sign-in", "bank", b.ID, "err", err)
		a.vaultDrop(vid)
		if isDown(err) {
			a.failSession(w, r, ls, "institution_down")
		} else {
			a.failSession(w, r, ls, "server_error")
		}
		return
	}
	drop := func(code string) {
		_ = a.bankRevoke(b, tok.ConsentID)
		a.vaultDrop(vid)
		a.failSession(w, r, ls, code)
	}
	keys, owner := []string{}, ""
	for _, ba := range g.Accounts {
		acc, err := a.world.FindAccount(ba.ID)
		if err != nil || acc.Institution != b.ID {
			continue
		}
		keys = append(keys, acc.Key)
		owner = acc.Owner
	}
	if len(keys) == 0 {
		drop("server_error")
		return
	}
	if ls.ConnectionID != "" {
		c := a.st.Connections[ls.ConnectionID]
		if c == nil || c.Status == "revoked" || c.PersonaID != owner {
			drop("access_denied")
			return
		}
	}
	slices.Sort(keys)
	code := newSecret("oc_", 16)
	ls.PersonaID, ls.Accounts, ls.BankAccounts = owner, keys, g.Accounts
	ls.BankConsentID, ls.ConsentEnds, ls.BankFrom = tok.ConsentID, g.Ends, g.From
	ls.Status = "authorised"
	ls.CodeHash = hashSecret(code)
	ls.CodeExpires = a.clock.Real().Add(codeLife)
	if err := a.saveLocked(); err != nil {
		a.log.Error("save state", "err", err)
	}
	a.redirectPartner(w, r, ls, url.Values{"code": {code}})
}
