package connect

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/clock"
)

const (
	widgetCookie     = "beerus_link"
	customerCookie   = "beerus_customer"
	otpLife          = 5 * time.Minute
	maxAttempts      = 5
	bankLockout      = 15 * time.Minute
	maxTrackedLogins = 10000
)

type page map[string]any

func (a *App) render(w http.ResponseWriter, name string, status int, data page) {
	data["ConnectBrand"] = a.cfg.Connect.BrandName
	data["ConnectName"] = a.cfg.Connect.BrandName
	data["Brand"] = a.cfg.Connect.BrandName
	data["BankClass"] = ""
	data["Ctx"] = "Open banking"
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
	a.render(w, "message", status, page{"Heading": heading, "Message": msg})
}

func (a *App) WidgetHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /assets/", assetHandler())
	mux.HandleFunc("GET /link", a.widgetOpen)
	mux.HandleFunc("GET /link/start", a.widgetStart)
	mux.HandleFunc("POST /link/institution", a.widgetInstitution)
	mux.HandleFunc("POST /link/cancel", a.widgetCancel)
	mux.HandleFunc("GET /bank/callback", a.bankCallback)
	mux.HandleFunc("POST /consent-events", a.consentEvents)
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
		a.beginBankAuth(w, r, ls)
		return
	}
	d := a.base(ls, ws)
	d["Institutions"] = a.openInstitutions()
	d["Title"] = "Connect your bank"
	a.render(w, "choose", http.StatusOK, d)
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
		if b := a.cfg.Bank(id); b == nil || b.Closed() {
			a.message(w, http.StatusBadRequest, "Unknown bank", "Go back and choose a bank from the list.")
			return
		}
		ls.Institution = id
		ls.Status = "bank_chosen"
		a.dirty()
	}
	a.beginBankAuth(w, r, ls)
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

func otp() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%06d", n.Int64())
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
		a.render(w, "customer_login", http.StatusOK, page{"Title": "Your connections", "Banks": a.openInstitutions()})
		return
	}
	owner := ws.LinkSessionID
	type row struct {
		ID, Partner, Bank, Status, Since string
		Active                           bool
	}
	rows := []row{}
	for _, c := range a.st.Connections {
		if customerKey(c.Institution, c.CustomerID) != owner {
			continue
		}
		bankName := c.Institution
		if b := a.cfg.Bank(c.Institution); b != nil {
			bankName = b.Name
		}
		p := a.partnerOf(c.PartnerID)
		name := c.PartnerID
		if p != nil {
			name = p.Name
		}
		rows = append(rows, row{ID: c.ID, Partner: name, Bank: bankName, Status: c.Status, Since: clock.DateOf(c.CreatedAt).String(), Active: c.Status != "revoked"})
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
	a.render(w, "customer", http.StatusOK, page{"Title": "Your connections", "Connections": rows, "CSRF": ws.CSRF})
}

func customerKey(bank, customer string) string {
	return bank + ":" + customer
}

func (a *App) customerLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		a.message(w, http.StatusBadRequest, "Request refused", "Try again.")
		return
	}
	bankID := r.PostForm.Get("bank")
	login := func() (bank.Customer, error) {
		l := a.obLedger(bankID)
		if l == nil {
			return bank.Customer{}, bank.ErrBadLogin
		}
		return l.Bank().Authenticate(l.ctx, r.PostForm.Get("username"), r.PostForm.Get("password"))
	}
	cust, err := login()
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		if !errors.Is(err, bank.ErrBadLogin) {
			a.log.Error("customer sign-in", "bank", bankID, "err", err)
		}
		a.render(w, "customer_login", http.StatusUnauthorized, page{"Title": "Your connections", "Banks": a.openInstitutions(), "Error": "The bank, username or password is wrong."})
		return
	}
	raw := newSecret("cs_", 20)
	ws := &WidgetSession{ID: hashSecret(raw), LinkSessionID: customerKey(bankID, cust.ID), CSRF: newSecret("", 16), ExpiresReal: a.clock.Real().Add(30 * time.Minute)}
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
	if c == nil || customerKey(c.Institution, c.CustomerID) != ws.LinkSessionID {
		a.message(w, http.StatusNotFound, "Not found", "That connection does not exist.")
		return
	}
	if c.Status != "revoked" {
		if err := a.revokeAtBank(c); err != nil {
			a.message(w, http.StatusServiceUnavailable, "Bank not reachable", "The bank could not be reached to withdraw access. Try again soon.")
			return
		}
	}
	a.setStatus(c, "revoked", "connection_revoked")
	http.Redirect(w, r, "/customer", http.StatusSeeOther)
}
