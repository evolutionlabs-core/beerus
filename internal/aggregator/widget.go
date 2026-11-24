package aggregator

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/view"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

const (
	widgetCookie     = "beerus_link"
	otpLife          = 5 * time.Minute
	maxAttempts      = 5
	bankLockout      = 15 * time.Minute
	maxTrackedLogins = 10000
	overlayMode      = "overlay"
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
	view.SetHeaders(w.Header())
	w.WriteHeader(status)
	if err := pages.ExecuteTemplate(w, name, data); err != nil {
		a.log.Error("render", "page", name, "err", err)
	}
}

func newSession(prefix string) (string, string, error) {
	raw, err := web.NewSecret(prefix, 20)
	if err != nil {
		return "", "", err
	}
	csrf, err := web.NewSecret("", 16)
	if err != nil {
		return "", "", err
	}
	return raw, csrf, nil
}

func (a *App) message(w http.ResponseWriter, status int, heading, msg string) {
	a.render(w, "message", status, page{"Heading": heading, "Message": msg})
}

func (a *App) WidgetHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /assets/", view.Assets())
	mux.Handle("GET /connect.js", view.Script("connect.js"))
	mux.HandleFunc("GET /link", a.widgetOpen)
	mux.HandleFunc("GET /link/start", a.registered(a.widgetStart))
	mux.HandleFunc("POST /link/institution", a.registered(a.widgetInstitution))
	mux.HandleFunc("GET "+pickPath, a.pickOpen)
	mux.HandleFunc("POST "+pickPath, a.pickSubmit)
	mux.HandleFunc("POST /link/cancel", a.widgetCancel)
	mux.HandleFunc("GET /bank/callback", a.bankCallback)
	mux.HandleFunc("POST /consent-events", a.consentEvents)
	return a.sameOrigin(a.framing(mux))
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

type framedWriter struct {
	http.ResponseWriter
	origin string
}

func (f *framedWriter) WriteHeader(status int) {
	view.AllowFrame(f.Header(), f.origin)
	f.ResponseWriter.WriteHeader(status)
}

func (a *App) framing(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := a.frameOrigin(r); origin != "" {
			w = &framedWriter{ResponseWriter: w, origin: origin}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) frameOrigin(r *http.Request) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var ls *LinkSession
	if r.URL.Path == "/link" {
		if r.URL.Query().Get("mode") != overlayMode {
			return ""
		}
		ls = a.linkByToken(r.URL.Query().Get("token"))
	} else if ck, err := r.Cookie(widgetCookie); err == nil {
		if ws := a.st.WidgetSessions[web.HashSecret(ck.Value)]; ws != nil {
			if s := a.st.LinkSessions[ws.LinkSessionID]; s != nil && s.Overlay {
				ls = s
			}
		}
	}
	if ls == nil {
		return ""
	}
	return partnerOrigin(ls.RedirectURI)
}

func partnerOrigin(redirectURI string) string {
	u, err := url.Parse(redirectURI)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.ContainsAny(u.Host, " ;,'\"") {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func (a *App) linkByToken(token string) *LinkSession {
	h := web.HashSecret(token)
	for _, s := range a.st.LinkSessions {
		if subtle.ConstantTimeCompare([]byte(s.TokenHash), []byte(h)) == 1 {
			return s
		}
	}
	return nil
}

func (a *App) setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteNoneMode, MaxAge: maxAge})
}

func (a *App) endWidgetLocked(w http.ResponseWriter, ls *LinkSession) {
	a.setCookie(w, widgetCookie, "", -1)
	for id, ws := range a.st.WidgetSessions {
		if ws.LinkSessionID == ls.ID {
			delete(a.st.WidgetSessions, id)
		}
	}
	a.dirty()
}

func (a *App) retryLocked(ls *LinkSession) {
	ls.BankState, ls.Verifier, ls.Status = "", "", "created"
	if !ls.Fixed {
		ls.Institution = ""
	}
	a.dirty()
}

type overlayError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type overlayAccount struct {
	AccountID string `json:"account_id"`
	Name      string `json:"name"`
	Mask      string `json:"mask"`
}

type overlayResult struct {
	PublicToken string            `json:"public_token,omitempty"`
	Picking     bool              `json:"picking,omitempty"`
	Institution map[string]string `json:"institution"`
	Accounts    []overlayAccount  `json:"accounts"`
	Error       *overlayError     `json:"error,omitempty"`
}

func overlayMessage(code string) string {
	switch code {
	case "access_denied":
		return "You did not approve access."
	case "institution_down":
		return "The bank is not responding. Try again in a moment."
	case "link_session_expired":
		return "This link has expired. Start again."
	}
	return "Something went wrong. Try again."
}

func (a *App) resultLocked(w http.ResponseWriter, name string, ls *LinkSession, params url.Values) {
	res := overlayResult{
		PublicToken: params.Get("public_token"),
		Picking:     params.Get("picking") != "",
		Institution: a.institutionJSON(ls.Institution),
		Accounts:    []overlayAccount{},
	}
	if code := params.Get("error"); code != "" {
		res.Error = &overlayError{Code: code, Message: overlayMessage(code)}
	}
	if res.PublicToken != "" {
		res.Accounts = a.pickedLocked(ls)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		a.log.Error("link result", "err", err)
		a.message(w, http.StatusInternalServerError, "Something went wrong", "Try again.")
		return
	}
	d := page{"Result": string(raw), "Parent": partnerOrigin(ls.RedirectURI)}
	if b := a.bank(ls.Institution); b != nil {
		d["Bank"] = b.Name
	}
	d["PartnerName"] = a.partnerOf(ls.PartnerID).Name
	a.render(w, name, http.StatusOK, d)
}

func (a *App) relayLocked(w http.ResponseWriter, ls *LinkSession, params url.Values) {
	a.resultLocked(w, "relay", ls, params)
}

func (a *App) redirectPartner(w http.ResponseWriter, r *http.Request, ls *LinkSession, params url.Values) {
	if ls.Overlay {
		a.endWidgetLocked(w, ls)
		a.relayLocked(w, ls, params)
		return
	}
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
	a.endWidgetLocked(w, ls)
	http.Redirect(w, r, u.String(), http.StatusSeeOther)
}

func (a *App) failSession(w http.ResponseWriter, r *http.Request, ls *LinkSession, code string) {
	if ls.Overlay && code == "institution_down" {
		a.retryLocked(ls)
		a.relayLocked(w, ls, url.Values{"error": {code}})
		return
	}
	if ls.Status != "exchanged" {
		ls.Status = "expired"
	}
	a.redirectPartner(w, r, ls, url.Values{"error": {code}})
}

func (a *App) widgetOpen(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ls := a.linkByToken(r.URL.Query().Get("token"))
	if ls == nil {
		a.message(w, http.StatusNotFound, "This link is not valid", "Go back to the app and start again.")
		return
	}
	if !ls.TokenUsed && ls.Status == "created" {
		ls.Overlay = r.URL.Query().Get("mode") == overlayMode
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
	raw, csrf, err := newSession("ws_")
	if err != nil {
		a.log.Error("widget session", "err", err)
		a.message(w, http.StatusInternalServerError, "Something went wrong", "Try again.")
		return
	}
	ls.TokenUsed = true
	ws := &WidgetSession{ID: web.HashSecret(raw), LinkSessionID: ls.ID, CSRF: csrf, ExpiresReal: ls.ExpiresReal}
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
	ws := a.st.WidgetSessions[web.HashSecret(ck.Value)]
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
	return page{"CSRF": ws.CSRF, "PartnerName": p.Name, "PartnerLegal": p.LegalName, "Purpose": p.Purpose, "Overlay": ls.Overlay, "Fixed": ls.Fixed, "Parent": partnerOrigin(ls.RedirectURI)}
}

func (a *App) widgetStart(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	ws, ls := a.session(w, r)
	if ls == nil {
		a.mu.Unlock()
		return
	}
	if ls.Institution != "" && !ls.Overlay {
		a.mu.Unlock()
		a.beginBankAuth(w, r, ls)
		return
	}
	defer a.mu.Unlock()
	d := a.base(ls, ws)
	banks := a.openInstitutions()
	if ls.Overlay && ls.Fixed {
		b := a.bank(ls.Institution)
		if b == nil || b.Closed() {
			a.message(w, http.StatusUnprocessableEntity, "This bank is not available", "Close this window and try again later.")
			return
		}
		banks = []*config.Bank{b}
	}
	d["Institutions"] = banks
	d["Title"] = "Connect your bank"
	a.render(w, "choose", http.StatusOK, d)
}

func (a *App) widgetInstitution(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	_, ls := a.session(w, r)
	if ls == nil {
		a.mu.Unlock()
		return
	}
	if ls.Institution == "" {
		id := r.PostForm.Get("institution")
		if b := a.bank(id); b == nil || b.Closed() {
			a.message(w, http.StatusBadRequest, "Unknown bank", "Go back and choose a bank from the list.")
			a.mu.Unlock()
			return
		}
		ls.Institution = id
		ls.Status = "bank_chosen"
		a.dirty()
	}
	a.mu.Unlock()
	a.beginBankAuth(w, r, ls)
}

func (a *App) widgetCancel(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	_, ls := a.session(w, r)
	if ls == nil {
		a.mu.Unlock()
		return
	}
	if ls.Status != "picking" {
		defer a.mu.Unlock()
		a.failSession(w, r, ls, "access_denied")
		return
	}
	b, consent := a.bank(ls.Institution), ls.BankConsentID
	a.vaultDrop(linkVaultPrefix + ls.ID)
	ls.Status = "expired"
	a.dirty()
	a.mu.Unlock()
	if b != nil {
		a.revokeOrQueue(b, consent)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.failSession(w, r, ls, "access_denied")
}

func otp() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%06d", n.Int64())
}
