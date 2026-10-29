package connect

import (
	"crypto/subtle"
	"errors"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

const bankCookie = "beerus_bank"

type bankSession struct {
	Persona    string
	CSRF       string
	Verified   bool
	OTP        string
	OTPExpires time.Time
	Attempts   int
	Expires    time.Time
}

type bankAuth struct {
	mu       sync.Mutex
	sessions map[string]*bankSession
	failures map[string]int
	locked   map[string]time.Time
}

type bankSite struct {
	*App
	bank *config.Bank
	auth *bankAuth
}

func (app *App) BankHandler(id string) http.Handler {
	b := app.cfg.Bank(id)
	if b == nil || app.ledgerFor(id) == nil {
		return nil
	}
	a := &bankSite{App: app, bank: b, auth: &bankAuth{sessions: map[string]*bankSession{}, failures: map[string]int{}, locked: map[string]time.Time{}}}
	mux := http.NewServeMux()
	mux.Handle("GET /assets/", assetHandler())
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/accounts", http.StatusSeeOther) })
	mux.HandleFunc("GET /login", a.obLoginPage)
	mux.HandleFunc("POST /login", a.obLoginSubmit)
	mux.HandleFunc("GET /verify", a.obVerifyPage)
	mux.HandleFunc("POST /verify", a.obVerifySubmit)
	mux.HandleFunc("POST /logout", a.obLogout)
	mux.HandleFunc("GET /accounts", a.obAccounts)
	mux.HandleFunc("GET /accounts/{nuban}", a.obStatement)
	mux.HandleFunc("GET /accounts/{nuban}/tx/{key}", a.obTx)
	mux.HandleFunc("GET /transfer", a.obTransferForm)
	mux.HandleFunc("POST /transfer/review", a.obTransferReview)
	mux.HandleFunc("POST /transfer/confirm", a.obTransferConfirm)
	mux.HandleFunc("GET /apps", a.obApps)
	mux.HandleFunc("POST /apps/{id}/revoke", a.obRevoke)
	return a.bankGuard(mux)
}

func (a *bankSite) bankGuard(next http.Handler) http.Handler {
	port := strconv.Itoa(a.bank.Port)
	hosts := map[string]bool{a.bank.ID + ".localhost:" + port: true, "127.0.0.1:" + port: true, "localhost:" + port: true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if host, _, err := net.SplitHostPort(r.RemoteAddr); err != nil || !net.ParseIP(host).IsLoopback() || !hosts[r.Host] {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost {
			if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host {
				http.Error(w, "cross-origin request refused", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *bankSite) bankInst() world.Institution {
	inst, _ := a.world.Institution(a.bank.ID)
	return inst
}

func (a *bankSite) bankRender(w http.ResponseWriter, name string, status int, s *bankSession, data roomPage) {
	inst := a.bankInst()
	data["Bank"] = inst
	data["Now"] = a.clock.Now()
	if s != nil && s.Verified {
		if p, ok := a.world.Persona(s.Persona); ok {
			data["User"] = p.Name
		}
		data["CSRF"] = s.CSRF
	}
	if _, ok := data["Title"]; !ok {
		data["Title"] = inst.Name
	}
	setPageHeaders(w)
	w.WriteHeader(status)
	if err := bankPages.ExecuteTemplate(w, name, data); err != nil {
		a.log.Error("render", "page", name, "err", err)
	}
}

func (a *bankSite) bankSessionFor(r *http.Request) (*bankSession, string) {
	ck, err := r.Cookie(bankCookie)
	if err != nil {
		return nil, ""
	}
	ba := a.auth
	ba.mu.Lock()
	defer ba.mu.Unlock()
	h := hashSecret(ck.Value)
	s := ba.sessions[h]
	if s == nil || a.clock.Real().After(s.Expires) {
		delete(ba.sessions, h)
		return nil, ""
	}
	return s, h
}

func (a *bankSite) bankUser(w http.ResponseWriter, r *http.Request) *bankSession {
	s, _ := a.bankSessionFor(r)
	if s == nil || !s.Verified {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return nil
	}
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil || subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(s.CSRF)) != 1 {
			http.Error(w, "the form expired; go back and try again", http.StatusForbidden)
			return nil
		}
	}
	return s
}

func (a *bankSite) obLoginPage(w http.ResponseWriter, r *http.Request) {
	a.bankRender(w, "bank_login", http.StatusOK, nil, roomPage{"Title": "Sign in"})
}

func (a *bankSite) obLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	user := strings.TrimSpace(r.PostForm.Get("username"))
	ba := a.auth
	real := a.clock.Real()
	ba.mu.Lock()
	until := ba.locked[user]
	ba.mu.Unlock()
	if real.Before(until) {
		a.bankRender(w, "bank_login", http.StatusTooManyRequests, nil, roomPage{"Title": "Sign in", "Error": "Too many attempts. Try again in 15 minutes."})
		return
	}
	p, ok := a.world.PersonaByUsername(user)
	good := ok && subtle.ConstantTimeCompare([]byte(p.Password), []byte(r.PostForm.Get("password"))) == 1
	if good && len(a.world.AccountsAt(p.ID, a.bank.ID)) == 0 {
		a.bankRender(w, "bank_login", http.StatusUnprocessableEntity, nil, roomPage{"Title": "Sign in", "Error": p.Name + " has no accounts at " + a.bankInst().Name + "."})
		return
	}
	if !good {
		ba.mu.Lock()
		ba.failures[user]++
		if ba.failures[user] >= maxAttempts {
			ba.locked[user] = real.Add(15 * time.Minute)
			ba.failures[user] = 0
		}
		ba.mu.Unlock()
		a.bankRender(w, "bank_login", http.StatusUnauthorized, nil, roomPage{"Title": "Sign in", "Error": "The username or password is wrong."})
		return
	}
	raw := newSecret("bs_", 20)
	s := &bankSession{Persona: p.ID, CSRF: newSecret("", 16), OTP: otp(), OTPExpires: real.Add(otpLife), Expires: real.Add(30 * time.Minute)}
	ba.mu.Lock()
	ba.failures[user] = 0
	ba.sessions[hashSecret(raw)] = s
	ba.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: bankCookie, Value: raw, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 1800})
	http.Redirect(w, r, "/verify", http.StatusSeeOther)
}

func (a *bankSite) obVerifyPage(w http.ResponseWriter, r *http.Request) {
	s, _ := a.bankSessionFor(r)
	if s == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	a.bankRender(w, "bank_verify", http.StatusOK, nil, roomPage{"Title": "Verify it's you", "OTP": s.OTP})
}

func (a *bankSite) obVerifySubmit(w http.ResponseWriter, r *http.Request) {
	s, h := a.bankSessionFor(r)
	if s == nil || r.ParseForm() != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	ba := a.auth
	ba.mu.Lock()
	defer ba.mu.Unlock()
	if a.clock.Real().After(s.OTPExpires) {
		delete(ba.sessions, h)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if subtle.ConstantTimeCompare([]byte(s.OTP), []byte(strings.TrimSpace(r.PostForm.Get("otp")))) != 1 {
		s.Attempts++
		if s.Attempts >= maxAttempts {
			delete(ba.sessions, h)
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		a.bankRender(w, "bank_verify", http.StatusUnauthorized, nil, roomPage{"Title": "Verify it's you", "OTP": s.OTP, "Error": "That code is wrong."})
		return
	}
	s.Verified = true
	s.OTP = ""
	http.Redirect(w, r, "/accounts", http.StatusSeeOther)
}

func (a *bankSite) obLogout(w http.ResponseWriter, r *http.Request) {
	if _, h := a.bankSessionFor(r); h != "" {
		ba := a.auth
		ba.mu.Lock()
		delete(ba.sessions, h)
		ba.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: bankCookie, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (a *bankSite) myAccounts(persona string) []acctView {
	now := a.clock.Now()
	out := []acctView{}
	for _, acc := range a.world.AccountsAt(persona, a.bank.ID) {
		out = append(out, a.acctView(acc, now))
	}
	return out
}

func (a *bankSite) myAccount(persona, nuban string) (world.Account, bool) {
	for _, acc := range a.world.AccountsAt(persona, a.bank.ID) {
		if acc.NUBAN == nuban {
			return acc, true
		}
	}
	return world.Account{}, false
}

func (a *bankSite) obAccounts(w http.ResponseWriter, r *http.Request) {
	s := a.bankUser(w, r)
	if s == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	accts := a.myAccounts(s.Persona)
	var total int64
	for _, v := range accts {
		total += v.Available
	}
	a.bankRender(w, "bank_accounts", http.StatusOK, s, roomPage{"Title": "Accounts", "Nav": "accounts", "Accounts": accts, "Total": total})
}

func (a *bankSite) obStatement(w http.ResponseWriter, r *http.Request) {
	s := a.bankUser(w, r)
	if s == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	acc, ok := a.myAccount(s.Persona, r.PathValue("nuban"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	now := a.clock.Now()
	ls, _ := a.statement(acc.Key, now)
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	const size = 30
	end := len(ls) - (page-1)*size
	if end < 0 {
		end = 0
	}
	start := max(end-size, 0)
	lines := []lineView{}
	for i := end - 1; i >= start; i-- {
		lv := a.lineView(acc, ls[i])
		lv.Href = "/accounts/" + acc.NUBAN + "/tx/" + url.PathEscape(ls[i].Key)
		if ls[i].Pending {
			lv.Href += "?pending=1"
		}
		lines = append(lines, lv)
	}
	a.bankRender(w, "bank_statement", http.StatusOK, s, roomPage{"Title": acc.Name, "Nav": "accounts", "A": a.acctView(acc, now), "Lines": lines, "Page": page, "Older": start > 0, "Newer": page > 1})
}

func (a *bankSite) obTx(w http.ResponseWriter, r *http.Request) {
	s := a.bankUser(w, r)
	if s == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	acc, ok := a.myAccount(s.Persona, r.PathValue("nuban"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	l, ok := a.line(acc.Key, r.PathValue("key"), r.URL.Query().Get("pending") == "1", a.clock.Now())
	if !ok {
		http.NotFound(w, r)
		return
	}
	related := []lineView{}
	if l.SessionID != "" {
		ls, _ := a.statement(acc.Key, a.clock.Now())
		for _, x := range ls {
			if x.SessionID == l.SessionID && x.Key != l.Key {
				lv := a.lineView(acc, x)
				lv.Href = "/accounts/" + acc.NUBAN + "/tx/" + url.PathEscape(x.Key)
				related = append(related, lv)
			}
		}
	}
	a.bankRender(w, "bank_tx", http.StatusOK, s, roomPage{"Title": Normalise(l.Narration), "Nav": "accounts", "A": a.acctView(acc, a.clock.Now()), "L": a.lineView(acc, l), "Related": related, "Done": r.URL.Query().Get("done") == "1"})
}

type payee struct {
	Key, Label string
}

func (a *bankSite) transferPage(s *bankSession, data roomPage) roomPage {
	data["Title"] = "Send money"
	data["Nav"] = "transfer"
	data["Accounts"] = a.myAccounts(s.Persona)
	own := []payee{}
	if p, ok := a.world.Persona(s.Persona); ok {
		for _, k := range p.Accounts {
			acc, _ := a.world.Account(k)
			inst, _ := a.world.Institution(acc.Institution)
			own = append(own, payee{Key: acc.Key, Label: inst.Name + " " + strings.Title(acc.Type) + " · " + acc.NUBAN})
		}
	}
	data["Own"] = own
	data["Banks"] = []string{"Bank A", "Bank B", "Bank C", "Bank D Microfinance"}
	return data
}

func (a *bankSite) obTransferForm(w http.ResponseWriter, r *http.Request) {
	s := a.bankUser(w, r)
	if s == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.bankRender(w, "bank_transfer", http.StatusOK, s, a.transferPage(s, roomPage{"From": r.URL.Query().Get("from")}))
}

func (a *bankSite) obTransferRequest(s *bankSession, f url.Values) (world.TransferRequest, error) {
	acc, ok := a.myAccount(s.Persona, f.Get("from"))
	if !ok {
		return world.TransferRequest{}, errors.New("Choose one of your accounts to pay from.")
	}
	amount, err := parseNaira(f.Get("amount"))
	if err != nil {
		return world.TransferRequest{}, errors.New("Enter an amount in naira, like 25000 or 2500.50.")
	}
	req := world.TransferRequest{From: acc.Key, Amount: amount, Narration: strings.TrimSpace(f.Get("narration"))}
	if f.Get("mode") == "own" {
		req.To = f.Get("own")
		if req.To == "" {
			return req, errors.New("Choose which of your accounts to pay into.")
		}
	} else {
		req.To = strings.TrimSpace(f.Get("number"))
		req.ToBank = f.Get("bank")
		req.ToName = strings.TrimSpace(f.Get("name"))
		if len(req.To) != 10 {
			return req, errors.New("Enter a 10-digit account number.")
		}
		if to, err := a.world.FindAccount(req.To); err == nil {
			inst, _ := a.world.Institution(to.Institution)
			if !strings.EqualFold(inst.Name, req.ToBank) {
				return req, errors.New("That account number is not at " + req.ToBank + ".")
			}
			req.To = to.Key
		}
	}
	return req, nil
}

func (a *bankSite) obTransferReview(w http.ResponseWriter, r *http.Request) {
	s := a.bankUser(w, r)
	if s == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	req, err := a.obTransferRequest(s, r.PostForm)
	var q world.TransferResult
	if err == nil {
		q, err = a.world.Quote(a.clock.Now(), req)
	}
	if err != nil {
		msg := err.Error()
		if errors.Is(err, world.ErrInsufficientFunds) {
			msg = "There isn't enough available money for this transfer and its charges."
		}
		a.bankRender(w, "bank_transfer", http.StatusUnprocessableEntity, s, a.transferPage(s, roomPage{"Error": msg, "From": r.PostForm.Get("from"), "Form": r.PostForm}))
		return
	}
	a.bankRender(w, "bank_review", http.StatusOK, s, roomPage{"Title": "Review transfer", "Nav": "transfer", "Q": q, "Form": r.PostForm})
}

func (a *bankSite) obTransferConfirm(w http.ResponseWriter, r *http.Request) {
	s := a.bankUser(w, r)
	if s == nil {
		return
	}
	a.mu.Lock()
	req, err := a.obTransferRequest(s, r.PostForm)
	var res world.TransferResult
	if err == nil {
		res, err = a.world.Transfer(a.clock.Now(), req)
	}
	if err != nil {
		msg := err.Error()
		if errors.Is(err, world.ErrInsufficientFunds) {
			msg = "There isn't enough available money for this transfer and its charges."
		}
		a.bankRender(w, "bank_transfer", http.StatusUnprocessableEntity, s, a.transferPage(s, roomPage{"Error": msg, "From": r.PostForm.Get("from"), "Form": r.PostForm}))
		a.mu.Unlock()
		return
	}
	a.dirty()
	a.mu.Unlock()
	a.SyncAll()
	from, _ := a.world.Account(res.From)
	http.Redirect(w, r, "/accounts/"+from.NUBAN+"/tx/"+url.PathEscape(res.LineKey)+"?done=1", http.StatusSeeOther)
}

type appRow struct {
	ID, Partner, Status string
	Since, Until        time.Time
	Accounts            []string
	LastAccess          time.Time
}

type accessRow struct {
	Partner, Account string
	At               time.Time
}

func (a *bankSite) obApps(w http.ResponseWriter, r *http.Request) {
	s := a.bankUser(w, r)
	if s == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	mine := map[string]string{}
	for _, acc := range a.world.AccountsAt(s.Persona, a.bank.ID) {
		mine[acc.Key] = strings.Title(acc.Type) + " · " + acc.NUBAN
	}
	rows := []appRow{}
	for _, c := range a.st.Connections {
		if c.PersonaID != s.Persona || c.Institution != a.bank.ID {
			continue
		}
		row := appRow{ID: c.ID, Partner: c.PartnerID, Status: c.Status, Since: c.AuthorisedAt, Until: c.ExpiresAt}
		if p := a.partnerOf(c.PartnerID); p != nil {
			row.Partner = p.Name
		}
		for _, id := range c.AccountIDs {
			if rec := a.st.Accounts[id]; rec != nil {
				row.Accounts = append(row.Accounts, mine[rec.WorldKey])
			}
		}
		for i := len(a.st.Access) - 1; i >= 0; i-- {
			if a.st.Access[i].ConnectionID == c.ID {
				row.LastAccess = a.st.Access[i].At
				break
			}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Since.After(rows[j].Since) })
	history := []accessRow{}
	for i := len(a.st.Access) - 1; i >= 0 && len(history) < 25; i-- {
		e := a.st.Access[i]
		label, ok := mine[e.WorldKey]
		if !ok {
			continue
		}
		name := e.PartnerID
		if p := a.partnerOf(e.PartnerID); p != nil {
			name = p.Name
		}
		history = append(history, accessRow{Partner: name, Account: label, At: e.At})
	}
	a.bankRender(w, "bank_apps", http.StatusOK, s, roomPage{"Title": "Connected apps", "Nav": "apps", "Apps": rows, "History": history, "Revoked": r.URL.Query().Get("revoked") == "1", "Via": a.cfg.Connect.BrandName})
}

func (a *bankSite) obRevoke(w http.ResponseWriter, r *http.Request) {
	s := a.bankUser(w, r)
	if s == nil {
		return
	}
	id := r.PathValue("id")
	a.mu.Lock()
	c := a.st.Connections[id]
	owned := c != nil && c.PersonaID == s.Persona && c.Institution == a.bank.ID
	a.mu.Unlock()
	if !owned {
		http.NotFound(w, r)
		return
	}
	_ = a.RevokeAtBank(id)
	a.poke()
	http.Redirect(w, r, "/apps?revoked=1", http.StatusSeeOther)
}

var bankPages = template.Must(template.New("").Funcs(uiFuncs).Funcs(template.FuncMap{
	"amtClass": func(v int64) string {
		if v > 0 {
			return "text-pos"
		}
		return "text-ink"
	},
	"title": strings.Title,
	"val": func(f url.Values, k string) string {
		if f == nil {
			return ""
		}
		return f.Get(k)
	},
}).Parse(bankTemplates))
