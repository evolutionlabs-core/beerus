package connect

import (
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

const (
	widgetCookie   = "beerus_link"
	customerCookie = "beerus_customer"
	otpLife        = 5 * time.Minute
	maxAttempts    = 5
)

type page map[string]any

func (a *App) render(w http.ResponseWriter, name string, status int, inst *world.Institution, data page) {
	data["ConnectBrand"] = a.cfg.Connect.BrandName
	data["ConnectName"] = a.cfg.Connect.BrandName
	if inst != nil {
		data["Brand"] = inst.Name
		data["BankClass"] = inst.ID
		data["Mark"] = inst.Name[len(inst.Name)-1:]
		data["Ctx"] = "Online banking"
	} else {
		data["Brand"] = a.cfg.Connect.BrandName
		data["BankClass"] = ""
		data["Ctx"] = "Open banking"
	}
	if _, ok := data["Title"]; !ok {
		data["Title"] = data["Brand"]
	}
	setPageHeaders(w)
	w.WriteHeader(status)
	if err := pages.ExecuteTemplate(w, name, data); err != nil {
		a.log.Error("render", "page", name, "err", err)
	}
}

func (a *App) message(w http.ResponseWriter, status int, heading, msg string) {
	a.render(w, "message", status, nil, page{"Heading": heading, "Message": msg})
}

func (a *App) WidgetHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /assets/", assetHandler())
	mux.HandleFunc("GET /link", a.widgetOpen)
	mux.HandleFunc("GET /link/start", a.widgetStart)
	mux.HandleFunc("POST /link/institution", a.widgetInstitution)
	mux.HandleFunc("POST /link/cancel", a.widgetCancel)
	mux.HandleFunc("GET /bank/login", a.bankLoginPage)
	mux.HandleFunc("POST /bank/login", a.bankLogin)
	mux.HandleFunc("GET /bank/otp", a.bankOTPPage)
	mux.HandleFunc("POST /bank/otp", a.bankOTP)
	mux.HandleFunc("GET /bank/consent", a.bankConsentPage)
	mux.HandleFunc("POST /bank/consent", a.bankConsent)
	mux.HandleFunc("GET /customer", a.customerPage)
	mux.HandleFunc("POST /customer/login", a.customerLogin)
	mux.HandleFunc("POST /customer/revoke", a.customerRevoke)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/customer", http.StatusSeeOther)
	})
	return a.sameOrigin(mux)
}

func (a *App) sameOrigin(next http.Handler) http.Handler {
	origin := a.cfg.WidgetURL()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if o := r.Header.Get("Origin"); o != "" && o != origin && o != "null" {
				http.Error(w, "cross-origin request refused", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: a.cfg.Connect.WidgetTLS, SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}

func (a *App) redirectPartner(w http.ResponseWriter, r *http.Request, ls *LinkSession, params url.Values) {
	u, err := url.Parse(ls.RedirectURI)
	if err != nil {
		a.message(w, http.StatusBadRequest, "Something went wrong", "The app's return address is not valid.")
		return
	}
	q := u.Query()
	for k, v := range params {
		q[k] = v
	}
	q.Set("link_session_id", ls.ID)
	if ls.State != "" {
		q.Set("state", ls.State)
	}
	u.RawQuery = q.Encode()
	a.setCookie(w, widgetCookie, "", -1)
	for id, ws := range a.st.WidgetSessions {
		if ws.LinkSessionID == ls.ID {
			delete(a.st.WidgetSessions, id)
		}
	}
	a.dirty()
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}

func (a *App) failSession(w http.ResponseWriter, r *http.Request, ls *LinkSession, code string) {
	if ls.Status != "exchanged" {
		ls.Status = "expired"
	}
	a.redirectPartner(w, r, ls, url.Values{"error": {code}})
}

func (a *App) widgetOpen(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	h := hashSecret(r.URL.Query().Get("token"))
	var ls *LinkSession
	for _, s := range a.st.LinkSessions {
		if subtle.ConstantTimeCompare([]byte(s.TokenHash), []byte(h)) == 1 {
			ls = s
			break
		}
	}
	if ls == nil {
		a.message(w, http.StatusNotFound, "This link is not valid", "Go back to the app and start again.")
		return
	}
	a.expireSession(ls)
	if ls.Status == "expired" || a.clock.Real().After(ls.TokenExpires) {
		a.failSession(w, r, ls, "link_session_expired")
		return
	}
	if ls.TokenUsed || ls.Status != "created" {
		a.message(w, http.StatusConflict, "This link was already used", "Go back to the app and start again.")
		return
	}
	ls.TokenUsed = true
	raw := newSecret("ws_", 20)
	ws := &WidgetSession{ID: hashSecret(raw), LinkSessionID: ls.ID, CSRF: newSecret("", 16), ExpiresReal: ls.ExpiresReal}
	a.st.WidgetSessions[ws.ID] = ws
	a.dirty()
	a.setCookie(w, widgetCookie, raw, int(linkSessionLife.Seconds()))
	http.Redirect(w, r, "/link/start", http.StatusSeeOther)
}

func (a *App) session(w http.ResponseWriter, r *http.Request) (*WidgetSession, *LinkSession) {
	ck, err := r.Cookie(widgetCookie)
	if err != nil {
		a.message(w, http.StatusUnauthorized, "Your session has ended", "Go back to the app and start again.")
		return nil, nil
	}
	ws := a.st.WidgetSessions[hashSecret(ck.Value)]
	if ws == nil || a.clock.Real().After(ws.ExpiresReal) {
		a.message(w, http.StatusUnauthorized, "Your session has ended", "Go back to the app and start again.")
		return nil, nil
	}
	ls := a.st.LinkSessions[ws.LinkSessionID]
	if ls == nil {
		a.message(w, http.StatusUnauthorized, "Your session has ended", "Go back to the app and start again.")
		return nil, nil
	}
	a.expireSession(ls)
	if ls.Status == "expired" {
		a.failSession(w, r, ls, "link_session_expired")
		return nil, nil
	}
	if ls.Status == "authorised" || ls.Status == "exchanged" {
		a.message(w, http.StatusConflict, "Already finished", "You can close this window.")
		return nil, nil
	}
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil || subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(ws.CSRF)) != 1 {
			a.message(w, http.StatusForbidden, "Request refused", "The form expired. Go back and try again.")
			return nil, nil
		}
	}
	return ws, ls
}

func (a *App) base(ls *LinkSession, ws *WidgetSession) page {
	p := a.partnerOf(ls.PartnerID)
	return page{"CSRF": ws.CSRF, "PartnerName": p.Name, "PartnerLegal": p.LegalName, "Purpose": p.Purpose}
}

func (a *App) widgetStart(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ws, ls := a.session(w, r)
	if ls == nil {
		return
	}
	if ls.Institution != "" {
		http.Redirect(w, r, "/bank/login", http.StatusSeeOther)
		return
	}
	d := a.base(ls, ws)
	d["Institutions"] = a.world.Institutions()
	d["Title"] = "Connect your bank"
	a.render(w, "choose", http.StatusOK, nil, d)
}

func (a *App) widgetInstitution(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ls := a.session(w, r)
	if ls == nil {
		return
	}
	if ls.Institution == "" {
		id := r.PostForm.Get("institution")
		if _, ok := a.world.Institution(id); !ok {
			a.message(w, http.StatusBadRequest, "Unknown bank", "Go back and choose a bank from the list.")
			return
		}
		ls.Institution = id
		ls.Status = "bank_chosen"
		a.dirty()
	}
	http.Redirect(w, r, "/bank/login", http.StatusSeeOther)
}

func (a *App) widgetCancel(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ls := a.session(w, r)
	if ls == nil {
		return
	}
	a.failSession(w, r, ls, "access_denied")
}

func (a *App) atBank(w http.ResponseWriter, r *http.Request) (*WidgetSession, *LinkSession, *world.Institution) {
	ws, ls := a.session(w, r)
	if ls == nil {
		return nil, nil, nil
	}
	inst, ok := a.world.Institution(ls.Institution)
	if !ok {
		http.Redirect(w, r, "/link/start", http.StatusSeeOther)
		return nil, nil, nil
	}
	if a.world.Down(inst.ID, a.clock.Now()) {
		a.failSession(w, r, ls, "institution_down")
		return nil, nil, nil
	}
	if ls.Status == "created" || ls.Status == "bank_chosen" {
		ls.Status = "at_bank"
		a.dirty()
	}
	return ws, ls, &inst
}

func (a *App) bankLoginPage(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ws, ls, inst := a.atBank(w, r)
	if ls == nil {
		return
	}
	d := a.base(ls, ws)
	a.render(w, "login", http.StatusOK, inst, d)
}

func otp() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%06d", n.Int64())
}

func (a *App) bankLogin(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ws, ls, inst := a.atBank(w, r)
	if ls == nil {
		return
	}
	d := a.base(ls, ws)
	p, ok := a.world.PersonaByUsername(r.PostForm.Get("username"))
	good := ok && subtle.ConstantTimeCompare([]byte(p.Password), []byte(r.PostForm.Get("password"))) == 1
	if good && len(a.world.AccountsAt(p.ID, inst.ID)) == 0 {
		d["Error"] = p.Name + " has no accounts at " + inst.Name + ". Go back and choose the bank that holds the account."
		a.render(w, "login", http.StatusUnprocessableEntity, inst, d)
		return
	}
	if good && ls.ConnectionID != "" {
		if c := a.st.Connections[ls.ConnectionID]; c == nil || c.Status == "revoked" {
			a.failSession(w, r, ls, "access_denied")
			return
		} else if c.PersonaID != p.ID {
			good = false
		}
	}
	if !good {
		ls.LoginAttempts++
		a.dirty()
		if ls.LoginAttempts >= maxAttempts {
			a.failSession(w, r, ls, "access_denied")
			return
		}
		d["Error"] = "The username or password is wrong."
		a.render(w, "login", http.StatusUnauthorized, inst, d)
		return
	}
	ls.PersonaID = p.ID
	ls.OTP = otp()
	ls.OTPExpires = a.clock.Real().Add(otpLife)
	ls.OTPAttempts = 0
	a.dirty()
	http.Redirect(w, r, "/bank/otp", http.StatusSeeOther)
}

func (a *App) bankOTPPage(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ws, ls, inst := a.atBank(w, r)
	if ls == nil {
		return
	}
	if ls.PersonaID == "" {
		http.Redirect(w, r, "/bank/login", http.StatusSeeOther)
		return
	}
	d := a.base(ls, ws)
	d["OTP"] = ls.OTP
	a.render(w, "otp", http.StatusOK, inst, d)
}

func (a *App) bankOTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ws, ls, inst := a.atBank(w, r)
	if ls == nil {
		return
	}
	if ls.PersonaID == "" {
		http.Redirect(w, r, "/bank/login", http.StatusSeeOther)
		return
	}
	d := a.base(ls, ws)
	d["OTP"] = ls.OTP
	if a.clock.Real().After(ls.OTPExpires) {
		ls.PersonaID = ""
		a.dirty()
		http.Redirect(w, r, "/bank/login", http.StatusSeeOther)
		return
	}
	if subtle.ConstantTimeCompare([]byte(ls.OTP), []byte(r.PostForm.Get("otp"))) != 1 {
		ls.OTPAttempts++
		a.dirty()
		if ls.OTPAttempts >= maxAttempts {
			a.failSession(w, r, ls, "access_denied")
			return
		}
		d["Error"] = "That code is wrong."
		a.render(w, "otp", http.StatusUnauthorized, inst, d)
		return
	}
	ls.Verified = true
	ls.OTP = ""
	a.dirty()
	http.Redirect(w, r, "/bank/consent", http.StatusSeeOther)
}

type consentAccount struct {
	Key, Name, Type, NUBAN string
	Checked                bool
}

func (a *App) consentData(ls *LinkSession, ws *WidgetSession) page {
	d := a.base(ls, ws)
	d["ConnectLegal"] = a.cfg.Connect.LegalName
	d["RegistryID"] = a.cfg.Connect.RegistryID
	d["CAC"] = a.cfg.Connect.CACNumber
	d["Start"] = ls.StartDate.String()
	if eff, cut := a.effectiveStart(ls.StartDate, ls.Institution); cut {
		d["Effective"] = eff.String()
	}
	d["Until"] = a.clock.Now().AddDate(1, 0, 0).Format("2 January 2006")
	existing := map[string]bool{}
	if c := a.st.Connections[ls.ConnectionID]; c != nil {
		for _, id := range c.AccountIDs {
			if rec := a.st.Accounts[id]; rec != nil {
				existing[rec.WorldKey] = true
			}
		}
	}
	accts := []consentAccount{}
	for _, acc := range a.world.AccountsAt(ls.PersonaID, ls.Institution) {
		accts = append(accts, consentAccount{Key: acc.Key, Name: acc.Name, Type: acc.Type, NUBAN: acc.NUBAN, Checked: existing[acc.Key] || len(existing) == 0})
	}
	d["Accounts"] = accts
	return d
}

func (a *App) bankConsentPage(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ws, ls, inst := a.atBank(w, r)
	if ls == nil {
		return
	}
	if !ls.Verified {
		http.Redirect(w, r, "/bank/login", http.StatusSeeOther)
		return
	}
	a.render(w, "consent", http.StatusOK, inst, a.consentData(ls, ws))
}

func (a *App) bankConsent(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ws, ls, inst := a.atBank(w, r)
	if ls == nil {
		return
	}
	if !ls.Verified {
		http.Redirect(w, r, "/bank/login", http.StatusSeeOther)
		return
	}
	if r.PostForm.Get("action") != "approve" {
		a.failSession(w, r, ls, "access_denied")
		return
	}
	allowed := []string{}
	for _, acc := range a.world.AccountsAt(ls.PersonaID, ls.Institution) {
		allowed = append(allowed, acc.Key)
	}
	chosen := []string{}
	for _, k := range r.PostForm["account"] {
		if slices.Contains(allowed, k) && !slices.Contains(chosen, k) {
			chosen = append(chosen, k)
		}
	}
	if len(chosen) == 0 {
		d := a.consentData(ls, ws)
		d["Error"] = "Choose at least one account."
		a.render(w, "consent", http.StatusBadRequest, inst, d)
		return
	}
	slices.Sort(chosen)
	code := newSecret("oc_", 16)
	ls.Accounts = chosen
	ls.Analytics = r.PostForm.Get("analytics") == "yes"
	ls.Status = "authorised"
	ls.CodeHash = hashSecret(code)
	ls.CodeExpires = a.clock.Real().Add(codeLife)
	if err := a.saveLocked(); err != nil {
		a.log.Error("save state", "err", err)
	}
	a.redirectPartner(w, r, ls, url.Values{"code": {code}})
}

func (a *App) customerPage(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ck, err := r.Cookie(customerCookie)
	var ws *WidgetSession
	if err == nil {
		ws = a.st.WidgetSessions[hashSecret(ck.Value)]
	}
	if ws == nil || a.clock.Real().After(ws.ExpiresReal) {
		a.render(w, "customer_login", http.StatusOK, nil, page{"Title": "Your connections"})
		return
	}
	persona := ws.LinkSessionID
	type row struct {
		ID, Partner, Bank, Status, Since string
		Active                           bool
	}
	rows := []row{}
	for _, c := range a.st.Connections {
		if c.PersonaID != persona {
			continue
		}
		inst, _ := a.world.Institution(c.Institution)
		p := a.partnerOf(c.PartnerID)
		name := c.PartnerID
		if p != nil {
			name = p.Name
		}
		rows = append(rows, row{ID: c.ID, Partner: name, Bank: inst.Name, Status: c.Status, Since: clock.DateOf(c.CreatedAt).String(), Active: c.Status != "revoked"})
	}
	slices.SortFunc(rows, func(x, y row) int {
		if x.Since == y.Since {
			return 0
		}
		if x.Since < y.Since {
			return 1
		}
		return -1
	})
	a.render(w, "customer", http.StatusOK, nil, page{"Title": "Your connections", "Connections": rows, "CSRF": ws.CSRF})
}

func (a *App) customerLogin(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := r.ParseForm(); err != nil {
		a.message(w, http.StatusBadRequest, "Request refused", "Try again.")
		return
	}
	p, ok := a.world.PersonaByUsername(r.PostForm.Get("username"))
	if !ok || subtle.ConstantTimeCompare([]byte(p.Password), []byte(r.PostForm.Get("password"))) != 1 {
		a.render(w, "customer_login", http.StatusUnauthorized, nil, page{"Title": "Your connections", "Error": "The username or password is wrong."})
		return
	}
	raw := newSecret("cs_", 20)
	ws := &WidgetSession{ID: hashSecret(raw), LinkSessionID: p.ID, CSRF: newSecret("", 16), ExpiresReal: a.clock.Real().Add(30 * time.Minute)}
	a.st.WidgetSessions[ws.ID] = ws
	a.dirty()
	a.setCookie(w, customerCookie, raw, 1800)
	http.Redirect(w, r, "/customer", http.StatusSeeOther)
}

func (a *App) customerRevoke(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ck, err := r.Cookie(customerCookie)
	if err != nil {
		http.Redirect(w, r, "/customer", http.StatusSeeOther)
		return
	}
	ws := a.st.WidgetSessions[hashSecret(ck.Value)]
	if ws == nil || a.clock.Real().After(ws.ExpiresReal) || r.ParseForm() != nil || subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(ws.CSRF)) != 1 {
		a.message(w, http.StatusForbidden, "Request refused", "The form expired. Go back and try again.")
		return
	}
	c := a.st.Connections[r.PostForm.Get("connection")]
	if c == nil || c.PersonaID != ws.LinkSessionID {
		a.message(w, http.StatusNotFound, "Not found", "That connection does not exist.")
		return
	}
	a.setStatus(c, "revoked", "connection_revoked")
	http.Redirect(w, r, "/customer", http.StatusSeeOther)
}
