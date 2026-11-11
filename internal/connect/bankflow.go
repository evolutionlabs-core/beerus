package connect

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"net/url"
	"slices"

	"github.com/evolutionlabs-core/beerus/internal/config"
)

func (a *App) beginBankAuth(w http.ResponseWriter, r *http.Request, ls *LinkSession) {
	a.mu.Lock()
	b := a.cfg.Bank(ls.Institution)
	if b == nil || b.APIPort == 0 {
		defer a.mu.Unlock()
		a.failSession(w, r, ls, "institution_down")
		return
	}
	verifier, challenge := pkcePair()
	state := newSecret("bs_", 16)
	form := a.parFormLocked(ls, state, challenge)
	a.mu.Unlock()
	uri, err := a.bankPAR(b, form)
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		if !isDown(err) {
			a.log.Error("pushed authorisation request", "bank", b.ID, "err", err)
			a.failSession(w, r, ls, "server_error")
			return
		}
		a.failSession(w, r, ls, "institution_down")
		return
	}
	if a.st.LinkSessions[ls.ID] != ls || ls.Status == "expired" || ls.Status == "authorised" || ls.Status == "exchanged" {
		a.message(w, http.StatusConflict, "Already finished", "You can close this window.")
		return
	}
	ls.BankState, ls.Verifier, ls.Status = state, verifier, "at_bank"
	a.dirty()
	q := url.Values{"client_id": {a.cfg.Connect.RegistryID}, "request_uri": {uri}}
	http.Redirect(w, r, b.URL()+"/authorize?"+q.Encode(), http.StatusSeeOther)
}

func (a *App) accountOwner(bankID, nuban string) (string, error) {
	l := a.obLedger(bankID)
	if l == nil {
		return "", fmt.Errorf("the books of %s are not open", bankID)
	}
	acc, err := l.Account(nuban)
	if err != nil {
		return "", fmt.Errorf("look up the owner of %s: %w", nuban, err)
	}
	return acc.Owner, nil
}

type bankCallbackInput struct {
	ls       *LinkSession
	bank     *config.Bank
	vid      string
	code     string
	verifier string
}

func (a *App) bankCallbackBeginLocked(w http.ResponseWriter, r *http.Request) (bankCallbackInput, bool) {
	_, ls := a.session(w, r)
	if ls == nil {
		return bankCallbackInput{}, false
	}
	q := r.URL.Query()
	if ls.BankState == "" || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(ls.BankState)) != 1 {
		a.message(w, http.StatusForbidden, "Request refused", "This sign-in does not match your session. Go back to the app and start again.")
		return bankCallbackInput{}, false
	}
	b := a.cfg.Bank(ls.Institution)
	verifier := ls.Verifier
	ls.BankState, ls.Verifier = "", ""
	a.dirty()
	if b == nil || q.Get("iss") != b.APIURL() {
		a.failSession(w, r, ls, "server_error")
		return bankCallbackInput{}, false
	}
	if e := q.Get("error"); e != "" {
		if e == "access_denied" {
			a.failSession(w, r, ls, e)
		} else {
			a.failSession(w, r, ls, "server_error")
		}
		return bankCallbackInput{}, false
	}
	return bankCallbackInput{ls: ls, bank: b, vid: linkVaultPrefix + ls.ID, code: q.Get("code"), verifier: verifier}, true
}

func (a *App) bankSignIn(in bankCallbackInput) (bankToken, bankGrant, string, error) {
	var g bankGrant
	tok, err := a.bankExchange(in.bank, in.code, in.verifier)
	if err != nil {
		return tok, g, "", err
	}
	a.mu.Lock()
	err = a.vaultPut(in.vid, tok)
	a.mu.Unlock()
	if err != nil {
		return tok, g, "", err
	}
	if err := a.bankGet(in.bank, in.vid, "/accounts", &g); err != nil {
		return tok, g, "", err
	}
	if len(g.Accounts) == 0 {
		return tok, g, "", nil
	}
	owner, err := a.accountOwner(in.bank.ID, g.Accounts[0].ID)
	return tok, g, owner, err
}

func (a *App) bankCallback(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	in, ok := a.bankCallbackBeginLocked(w, r)
	a.mu.Unlock()
	if !ok {
		return
	}
	tok, g, owner, err := a.bankSignIn(in)
	a.mu.Lock()
	if err != nil {
		defer a.mu.Unlock()
		a.log.Error("bank sign-in", "bank", in.bank.ID, "err", err)
		a.vaultDrop(in.vid)
		if isDown(err) {
			a.failSession(w, r, in.ls, "institution_down")
		} else {
			a.failSession(w, r, in.ls, "server_error")
		}
		return
	}
	if a.st.LinkSessions[in.ls.ID] != in.ls || in.ls.Status != "at_bank" {
		a.vaultDrop(in.vid)
		a.mu.Unlock()
		a.revokeOrQueue(in.bank, tok.ConsentID)
		a.message(w, http.StatusConflict, "Already finished", "This sign-in is no longer needed. Go back to the app and start again.")
		return
	}
	keys := []string{}
	for _, ba := range g.Accounts {
		keys = append(keys, acctKey(in.bank.ID, ba.ID))
	}
	code := ""
	if len(keys) == 0 {
		code = "server_error"
	} else if in.ls.ConnectionID != "" {
		if c := a.st.Connections[in.ls.ConnectionID]; c == nil || c.Status == "revoked" || c.CustomerID != owner {
			code = "access_denied"
		}
	}
	if code != "" {
		a.vaultDrop(in.vid)
		a.mu.Unlock()
		a.revokeOrQueue(in.bank, tok.ConsentID)
		a.mu.Lock()
		defer a.mu.Unlock()
		a.failSession(w, r, in.ls, code)
		return
	}
	defer a.mu.Unlock()
	slices.Sort(keys)
	secret := newSecret("oc_", 16)
	in.ls.CustomerID, in.ls.Accounts, in.ls.BankAccounts = owner, keys, g.Accounts
	in.ls.BankConsentID, in.ls.ConsentEnds, in.ls.BankFrom = tok.ConsentID, g.Ends, g.From
	in.ls.Status = "authorised"
	in.ls.CodeHash = hashSecret(secret)
	in.ls.CodeExpires = a.clock.Real().Add(codeLife)
	if err := a.saveLocked(); err != nil {
		a.log.Error("save state", "err", err)
	}
	a.redirectPartner(w, r, in.ls, url.Values{"code": {secret}})
}
