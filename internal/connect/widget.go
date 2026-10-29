package connect

import (
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"html/template"
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

var pages = template.Must(template.New("").Funcs(template.FuncMap{
	"naira": func(minor int64) string {
		sign := ""
		if minor < 0 {
			sign, minor = "-", -minor
		}
		n := fmt.Sprintf("%d", minor/100)
		out := ""
		for i, c := range n {
			if i > 0 && (len(n)-i)%3 == 0 {
				out += ","
			}
			out += string(c)
		}
		return fmt.Sprintf("%s₦%s.%02d", sign, out, minor%100)
	},
}).Parse(`
{{define "head"}}<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Title}}</title>
<style>
:root{--ink:#14171f;--muted:#5b6170;--line:#dfe2e8;--bg:#f6f7f9;--card:#fff;--accent:{{.Accent}}}
*{box-sizing:border-box}body{margin:0;font:15px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;color:var(--ink);background:var(--bg)}
.sim{background:#b42318;color:#fff;text-align:center;font-weight:700;letter-spacing:.08em;padding:6px 12px;font-size:12px}
.wrap{max-width:480px;margin:24px auto;padding:0 16px}.card{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:24px}
.brand{display:flex;align-items:center;gap:10px;margin-bottom:16px}.logo{width:36px;height:36px;border-radius:8px;background:var(--accent);color:#fff;display:grid;place-items:center;font-weight:700}
h1{font-size:20px;margin:0 0 12px}p{margin:0 0 12px}.muted{color:var(--muted);font-size:13px}
label{display:block;font-weight:600;margin:12px 0 4px}input[type=text],input[type=password]{width:100%;padding:10px;border:1px solid var(--line);border-radius:8px;font:inherit}
button{font:inherit;font-weight:600;padding:10px 16px;border-radius:8px;border:1px solid var(--accent);background:var(--accent);color:#fff;cursor:pointer;width:100%;margin-top:16px}
button.secondary{background:#fff;color:var(--ink);border-color:var(--line)}
.bank{display:flex;justify-content:space-between;align-items:center;width:100%;text-align:left;background:#fff;color:var(--ink);border:1px solid var(--line);margin-top:8px}
.acct{display:flex;gap:10px;align-items:flex-start;border:1px solid var(--line);border-radius:8px;padding:10px;margin-top:8px;font-weight:400}
dl{display:grid;grid-template-columns:auto 1fr;gap:4px 12px;margin:12px 0;font-size:14px}dt{color:var(--muted)}dd{margin:0}
.inbox{background:#fff8e1;border:1px dashed #c99a00;border-radius:8px;padding:10px;font-size:13px;margin:12px 0}.err{color:#b42318;font-weight:600}
table{width:100%;border-collapse:collapse;font-size:14px}td{padding:8px 0;border-top:1px solid var(--line)}
</style></head><body><div class="sim">SIMULATOR · NOT A REAL BANK · NO REAL MONEY</div><div class="wrap"><div class="card">
<div class="brand"><div class="logo">{{.Logo}}</div><div><strong>{{.Brand}}</strong><div class="muted">{{.Sub}}</div></div></div>{{end}}
{{define "foot"}}</div><p class="muted" style="text-align:center;margin-top:12px">Beerus simulator. Fictional institutions and people.</p></div></body></html>{{end}}

{{define "choose"}}{{template "head" .}}
<h1>Connect your bank to {{.PartnerName}}</h1>
<p class="muted">{{.ConnectName}} lets {{.PartnerName}} read your account details, balances and transactions. You approve access at your bank.</p>
{{range .Institutions}}<form method="post" action="/link/institution"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="institution" value="{{.ID}}"><button class="bank" type="submit"><span>{{.Name}}</span><span class="muted">{{.Style}} bank →</span></button></form>{{end}}
<form method="post" action="/link/cancel"><input type="hidden" name="csrf" value="{{.CSRF}}"><button class="secondary" type="submit">Cancel</button></form>
{{template "foot" .}}{{end}}

{{define "login"}}{{template "head" .}}
<h1>Sign in to {{.Brand}}</h1>
<p class="muted">{{.ConnectName}} sent you here to approve access for {{.PartnerName}}. Enter your internet banking details.</p>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<form method="post" action="/bank/login"><input type="hidden" name="csrf" value="{{.CSRF}}">
<label for="u">Username</label><input id="u" type="text" name="username" autocomplete="username" required>
<label for="p">Password</label><input id="p" type="password" name="password" autocomplete="current-password" required>
<button type="submit">Sign in</button></form>
<form method="post" action="/link/cancel"><input type="hidden" name="csrf" value="{{.CSRF}}"><button class="secondary" type="submit">Cancel</button></form>
{{template "foot" .}}{{end}}

{{define "otp"}}{{template "head" .}}
<h1>Enter your one-time code</h1>
<p>We sent a 6-digit code to the phone number on your account.</p>
<div class="inbox"><strong>Simulator inbox</strong><br>{{.Brand}}: your code is <strong>{{.OTP}}</strong>. It expires in 5 minutes. Never share it.</div>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<form method="post" action="/bank/otp"><input type="hidden" name="csrf" value="{{.CSRF}}">
<label for="o">Code</label><input id="o" type="text" name="otp" inputmode="numeric" autocomplete="one-time-code" required>
<button type="submit">Continue</button></form>
{{template "foot" .}}{{end}}

{{define "consent"}}{{template "head" .}}
<h1>Share your account data</h1>
<p><strong>{{.ConnectBrand}}</strong>, on behalf of <strong>{{.PartnerName}}</strong>, {{.Purpose}}.</p>
<dl>
<dt>Receiving party</dt><dd>{{.ConnectLegal}} ({{.ConnectBrand}})</dd>
<dt>Registry number</dt><dd>{{.RegistryID}}</dd>
<dt>CAC number</dt><dd>{{.CAC}}</dd>
<dt>Acting for</dt><dd>{{.PartnerLegal}} ({{.PartnerName}})</dd>
<dt>Access</dt><dd>Account details, balances and transactions. Read only.</dd>
<dt>History from</dt><dd>{{.Start}}{{if .Effective}} (this bank keeps history from {{.Effective}}){{end}}</dd>
<dt>Duration</dt><dd>12 months, until {{.Until}}</dd>
<dt>Frequency</dt><dd>Automatic updates during the day, and when {{.PartnerName}} asks</dd>
</dl>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<form method="post" action="/bank/consent"><input type="hidden" name="csrf" value="{{.CSRF}}">
<label>Accounts to share</label>
{{range .Accounts}}<label class="acct"><input type="checkbox" name="account" value="{{.Key}}" {{if .Checked}}checked{{end}}><span>{{.Name}}<br><span class="muted">{{.Type}} · {{.NUBAN}}</span></span></label>{{end}}
<label class="acct"><input type="checkbox" name="analytics" value="yes"><span>Allow anonymised analytics<br><span class="muted">Optional. You can say no and still connect.</span></span></label>
<p class="muted">You can withdraw access at any time in {{.Brand}}'s connected apps page or on {{.ConnectBrand}}'s customer page. When you withdraw, {{.ConnectBrand}} stops collecting data and deletes what it holds within 30 days. {{.PartnerName}} keeps records already in your books under its own privacy notice.</p>
<button type="submit" name="action" value="approve">Approve</button>
<button class="secondary" type="submit" name="action" value="decline">Decline</button>
</form>
{{template "foot" .}}{{end}}

{{define "message"}}{{template "head" .}}<h1>{{.Heading}}</h1><p>{{.Message}}</p>{{template "foot" .}}{{end}}

{{define "customer_login"}}{{template "head" .}}
<h1>Your connections</h1><p class="muted">Sign in with your bank username and password to see and revoke the apps that read your accounts through {{.ConnectBrand}}.</p>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
<form method="post" action="/customer/login">
<label for="u">Username</label><input id="u" type="text" name="username" required>
<label for="p">Password</label><input id="p" type="password" name="password" required>
<button type="submit">Sign in</button></form>
{{template "foot" .}}{{end}}

{{define "customer"}}{{template "head" .}}
<h1>Your connections</h1>
{{if not .Connections}}<p>No apps are connected.</p>{{end}}
<table>{{range .Connections}}<tr><td><strong>{{.Partner}}</strong> via {{$.ConnectBrand}}<br><span class="muted">{{.Bank}} · {{.Status}} · since {{.Since}}</span></td><td style="width:120px">{{if .Active}}<form method="post" action="/customer/revoke"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="connection" value="{{.ID}}"><button class="secondary" type="submit" style="margin:0">Revoke</button></form>{{end}}</td></tr>{{end}}</table>
{{template "foot" .}}{{end}}
`))

type page map[string]any

func (a *App) render(w http.ResponseWriter, name string, status int, inst *world.Institution, data page) {
	data["ConnectName"] = a.cfg.Connect.BrandName
	data["ConnectBrand"] = a.cfg.Connect.BrandName
	if inst != nil {
		data["Brand"] = inst.Name
		data["Sub"] = "Internet banking"
		data["Logo"] = inst.Name[len(inst.Name)-1:]
		data["Accent"] = template.CSS("#1d3a8a")
		if inst.Style == "digital" {
			data["Accent"] = template.CSS("#6b2bd9")
		}
	} else {
		data["Brand"] = a.cfg.Connect.BrandName
		data["Sub"] = "Open banking"
		data["Logo"] = "B"
		data["Accent"] = template.CSS("#0f766e")
	}
	if _, ok := data["Title"]; !ok {
		data["Title"] = data["Brand"]
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self' http: https:; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
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
	good := ok && subtle.ConstantTimeCompare([]byte(p.Password), []byte(r.PostForm.Get("password"))) == 1 && len(a.world.AccountsAt(p.ID, inst.ID)) > 0
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
