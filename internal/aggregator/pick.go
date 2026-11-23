package aggregator

import (
	"net/http"
	"net/url"
	"slices"

	"github.com/evolutionlabs-core/beerus/internal/web"
)

const (
	pickPath   = "/link/accounts"
	maskDigits = 4
)

type pickRow struct {
	NUBAN   string
	Name    string
	Mask    string
	Checked bool
}

func mask(nuban string) string {
	if len(nuban) <= maskDigits {
		return nuban
	}
	return nuban[len(nuban)-maskDigits:]
}

func (a *App) pickedLocked(ls *LinkSession) []overlayAccount {
	out := []overlayAccount{}
	for _, ba := range ls.BankAccounts {
		if slices.Contains(ls.Accounts, acctKey(ls.Institution, ba.ID)) {
			out = append(out, overlayAccount{AccountID: AccountID(ls.PartnerID, ls.Institution, ba.ID), Name: ba.Name, Mask: mask(ba.ID)})
		}
	}
	return out
}

func (a *App) pickPageLocked(w http.ResponseWriter, ws *WidgetSession, ls *LinkSession, status int, ticked []string) {
	d := a.base(ls, ws)
	rows := []pickRow{}
	for _, ba := range ls.BankAccounts {
		rows = append(rows, pickRow{NUBAN: ba.ID, Name: ba.Name, Mask: mask(ba.ID), Checked: slices.Contains(ticked, ba.ID)})
	}
	d["Rows"] = rows
	d["Ticked"] = len(ticked)
	d["Title"] = "Pick accounts"
	if b := a.bank(ls.Institution); b != nil {
		d["Bank"] = b.Name
	}
	if len(ticked) == 0 {
		d["Error"] = "Pick at least one account to continue."
	}
	a.render(w, "pick", status, d)
}

func (a *App) pickOpen(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ws, ls := a.session(w, r)
	if ls == nil {
		return
	}
	if ls.Status != "picking" {
		a.message(w, http.StatusConflict, "Nothing to pick", "Go back to the app and start again.")
		return
	}
	all := []string{}
	for _, ba := range ls.BankAccounts {
		all = append(all, ba.ID)
	}
	a.pickPageLocked(w, ws, ls, http.StatusOK, all)
}

func (a *App) pickSubmit(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ws, ls := a.session(w, r)
	if ls == nil {
		return
	}
	if ls.Status != "picking" {
		a.message(w, http.StatusConflict, "Nothing to pick", "Go back to the app and start again.")
		return
	}
	keys := []string{}
	for _, nuban := range r.PostForm["account"] {
		key := acctKey(ls.Institution, nuban)
		if !slices.Contains(ls.Accounts, key) {
			a.message(w, http.StatusBadRequest, "Unknown account", "Go back and pick from the list.")
			return
		}
		if !slices.Contains(keys, key) {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		a.pickPageLocked(w, ws, ls, http.StatusUnprocessableEntity, nil)
		return
	}
	secret, err := web.NewSecret("oc_", 16)
	if err != nil {
		a.log.Error("link code", "err", err)
		a.failSession(w, r, ls, "server_error")
		return
	}
	slices.Sort(keys)
	ls.Accounts = keys
	ls.Status = "authorised"
	ls.CodeHash = web.HashSecret(secret)
	ls.CodeExpires = a.clock.Real().Add(codeLife)
	if err := a.saveLocked(); err != nil {
		a.log.Error("save state", "err", err)
	}
	if ls.Overlay {
		a.endWidgetLocked(w, ls)
		a.resultLocked(w, "connected", ls, url.Values{"code": {secret}})
		return
	}
	a.redirectPartner(w, r, ls, url.Values{"code": {secret}})
}
