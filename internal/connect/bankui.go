package connect

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

const bankCookie = "beerus_bank"

type bankSession struct {
	Customer   string
	Name       string
	CSRF       string
	Verified   bool
	OTP        string
	OTPExpires time.Time
	Attempts   int
	Expires    time.Time
	Fresh      string
}

type bankFailures struct {
	Count int
	First time.Time
}

type bankAuth struct {
	mu       sync.Mutex
	sessions map[string]*bankSession
	failures map[string]bankFailures
	swept    time.Time
	locked   map[string]time.Time
}

type bankSite struct {
	*App
	bank *config.Bank
	auth *bankAuth
	ob   *openbanking.Server
}

func (app *App) BankHandler(id string) http.Handler {
	app.mu.Lock()
	b := app.cfg.Bank(id)
	have := app.ledgerFor(id) != nil
	app.mu.Unlock()
	if b == nil || !have {
		return nil
	}
	a := &bankSite{App: app, bank: b, ob: app.openBanking(id), auth: &bankAuth{sessions: map[string]*bankSession{}, failures: map[string]bankFailures{}, locked: map[string]time.Time{}}}
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
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("POST /internal/revoke", a.internalRevoke)
	mux.HandleFunc("POST /internal/pause", a.internalPause)
	mux.HandleFunc("POST /internal/resume", a.internalResume)
	mux.HandleFunc("GET /authorize", a.obAuthorize)
	mux.HandleFunc("GET /authorize/consent", a.obConsentPage)
	mux.HandleFunc("POST /authorize/consent", a.obConsentSubmit)
	return a.bankGuard(mux)
}

func (a *bankSite) bankGuard(next http.Handler) http.Handler {
	port := strconv.Itoa(a.bank.Port)
	hosts := map[string]bool{a.bank.ID + ".localhost:" + port: true, "127.0.0.1:" + port: true, "localhost:" + port: true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		if host, _, err := net.SplitHostPort(r.RemoteAddr); (!remotePeers() && (err != nil || !net.ParseIP(host).IsLoopback())) || !hosts[r.Host] {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/internal/") {
			next.ServeHTTP(w, r)
			return
		}
		a.freshen()
		if a.role == RoleBank && (!a.synced.Load() || a.paused.Load()) && !strings.HasPrefix(r.URL.Path, "/assets/") {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		if a.bankPhase(a.bank.ID) == PhaseSuspended && !strings.HasPrefix(r.URL.Path, "/assets/") {
			a.bankRender(w, "bank_unavailable", http.StatusServiceUnavailable, nil, roomPage{"Title": a.bank.Name})
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

func (a *bankSite) bankRender(w http.ResponseWriter, name string, status int, s *bankSession, data roomPage) {
	data["Bank"] = a.bank
	data["Now"] = a.clock.Now()
	if s != nil && s.Verified {
		data["User"] = s.Name
		data["CSRF"] = s.CSRF
	}
	if _, ok := data["Title"]; !ok {
		data["Title"] = a.bank.Name
	}
	setPageHeaders(w)
	w.WriteHeader(status)
	if err := bankPages.ExecuteTemplate(w, name, data); err != nil {
		a.log.Error("render", "page", name, "err", err)
	}
}

func (a *bankSite) bankFail(w http.ResponseWriter, r *http.Request, err error) {
	a.log.Error("online banking", "bank", a.bank.ID, "path", r.URL.Path, "err", err)
	http.Error(w, "Something went wrong at the bank. Try again soon.", http.StatusInternalServerError)
}

func (a *bankSite) book() (book, error) {
	bk, ok := a.bookOf(a.bank.ID)
	if !ok {
		return book{}, fmt.Errorf("the books of %s are not open", a.bank.Name)
	}
	return bk, nil
}

func (ba *bankAuth) sweepLocked(real time.Time) {
	if real.Sub(ba.swept) < time.Minute {
		return
	}
	ba.swept = real
	for k, f := range ba.failures {
		if real.Sub(f.First) > bankLockout {
			delete(ba.failures, k)
		}
	}
	for k, until := range ba.locked {
		if !real.Before(until) {
			delete(ba.locked, k)
		}
	}
	for k, s := range ba.sessions {
		if real.After(s.Expires) {
			delete(ba.sessions, k)
		}
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
	if !bank.ValidUsername(user) {
		a.bankRender(w, "bank_login", http.StatusUnauthorized, nil, roomPage{"Title": "Sign in", "Error": "The username or password is wrong."})
		return
	}
	ba := a.auth
	real := a.clock.Real()
	ba.mu.Lock()
	ba.sweepLocked(real)
	until := ba.locked[user]
	full := len(ba.failures)+len(ba.locked) >= maxTrackedLogins
	ba.mu.Unlock()
	if full {
		a.bankRender(w, "bank_login", http.StatusTooManyRequests, nil, roomPage{"Title": "Sign in", "Error": fmt.Sprintf("Too many sign-in attempts are being tracked (%d). Try again in 15 minutes.", maxTrackedLogins)})
		return
	}
	if real.Before(until) {
		a.bankRender(w, "bank_login", http.StatusTooManyRequests, nil, roomPage{"Title": "Sign in", "Error": "Too many attempts. Try again in 15 minutes."})
		return
	}
	bk, err := a.book()
	if err != nil {
		a.bankFail(w, r, err)
		return
	}
	c, err := bk.ls.Bank().Authenticate(r.Context(), user, r.PostForm.Get("password"))
	if errors.Is(err, bank.ErrBadLogin) {
		ba.mu.Lock()
		f := ba.failures[user]
		if f.Count == 0 {
			f.First = real
		}
		f.Count++
		if f.Count >= maxAttempts {
			ba.locked[user] = real.Add(bankLockout)
			delete(ba.failures, user)
		} else {
			ba.failures[user] = f
		}
		ba.mu.Unlock()
		a.bankRender(w, "bank_login", http.StatusUnauthorized, nil, roomPage{"Title": "Sign in", "Error": "The username or password is wrong."})
		return
	}
	if err != nil {
		a.bankFail(w, r, err)
		return
	}
	raw := newSecret("bs_", 20)
	s := &bankSession{Customer: c.ID, Name: c.Name, CSRF: newSecret("", 16), OTP: otp(), OTPExpires: real.Add(otpLife), Expires: real.Add(30 * time.Minute)}
	ba.mu.Lock()
	delete(ba.failures, user)
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
	if ck, err := r.Cookie(oauthCookie); err == nil && ck.Value != "" {
		if v, err := url.ParseQuery(ck.Value); err == nil {
			s.Fresh = v.Get("request_uri")
			http.Redirect(w, r, "/authorize/consent", http.StatusSeeOther)
			return
		}
	}
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

func (a *bankSite) myAccounts(ctx context.Context, customer string) (book, []acctView, error) {
	bk, err := a.book()
	if err != nil {
		return book{}, nil, err
	}
	accts, err := bk.ls.Bank().CustomerAccounts(ctx, customer)
	if err != nil {
		return book{}, nil, err
	}
	out := []acctView{}
	for _, acc := range accts {
		v, err := bk.acctView(ctx, acc)
		if err != nil {
			return book{}, nil, err
		}
		out = append(out, v)
	}
	return bk, out, nil
}

func (a *bankSite) myAccount(ctx context.Context, customer, nuban string) (book, acctView, bool, error) {
	bk, accts, err := a.myAccounts(ctx, customer)
	if err != nil {
		return book{}, acctView{}, false, err
	}
	for _, v := range accts {
		if v.NUBAN == nuban {
			return bk, v, true, nil
		}
	}
	return bk, acctView{}, false, nil
}

func (a *bankSite) obAccounts(w http.ResponseWriter, r *http.Request) {
	s := a.bankUser(w, r)
	if s == nil {
		return
	}
	_, accts, err := a.myAccounts(r.Context(), s.Customer)
	if err != nil {
		a.bankFail(w, r, err)
		return
	}
	var total int64
	for _, v := range accts {
		total += v.Available
	}
	a.bankRender(w, "bank_accounts", http.StatusOK, s, roomPage{"Title": "Accounts", "Nav": "accounts", "Accounts": accts, "Total": total})
}

func bankHref(l bank.FeedLine) string {
	return "/accounts/" + l.NUBAN + "/tx/" + url.PathEscape(l.Key)
}

type bankLineView struct {
	bank.FeedLine
	Description string
	Href        string
}

func (a *bankSite) obStatement(w http.ResponseWriter, r *http.Request) {
	s := a.bankUser(w, r)
	if s == nil {
		return
	}
	ctx := r.Context()
	bk, acc, ok, err := a.myAccount(ctx, s.Customer, r.PathValue("nuban"))
	if err != nil {
		a.bankFail(w, r, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	cursor := r.URL.Query().Get("cursor")
	ls, next, err := bk.ls.Bank().Recent(ctx, acc.NUBAN, 30, cursor)
	if errors.Is(err, bank.ErrBadCursor) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		a.bankFail(w, r, err)
		return
	}
	if err := a.markRemoved(ctx, ls); err != nil {
		a.bankFail(w, r, err)
		return
	}
	lines := []bankLineView{}
	for _, l := range ls {
		lines = append(lines, bankLineView{FeedLine: l, Description: Normalise(l.Narration), Href: bankHref(l)})
	}
	a.bankRender(w, "bank_statement", http.StatusOK, s, roomPage{"Title": acc.Name, "Nav": "accounts", "A": acc, "Lines": lines, "Next": next, "First": cursor != "", "Transfer": transferOutcome(r.URL.Query().Get("transfer"))})
}

func (a *bankSite) obTx(w http.ResponseWriter, r *http.Request) {
	s := a.bankUser(w, r)
	if s == nil {
		return
	}
	ctx := r.Context()
	bk, acc, ok, err := a.myAccount(ctx, s.Customer, r.PathValue("nuban"))
	if err != nil {
		a.bankFail(w, r, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	l, err := bk.ls.Bank().Line(ctx, acc.NUBAN, r.PathValue("key"))
	if errors.Is(err, bank.ErrUnknownLine) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		a.bankFail(w, r, err)
		return
	}
	if err := a.markRemoved(ctx, []bank.FeedLine{l}); err != nil {
		a.bankFail(w, r, err)
		return
	}
	related := []bankLineView{}
	if l.SessionID != "" {
		ls, err := bk.ls.Bank().SessionLines(ctx, l.SessionID)
		if err != nil {
			a.bankFail(w, r, err)
			return
		}
		if err := a.markRemoved(ctx, ls); err != nil {
			a.bankFail(w, r, err)
			return
		}
		for _, x := range ls {
			if x.NUBAN == acc.NUBAN && x.Key != l.Key {
				related = append(related, bankLineView{FeedLine: x, Description: Normalise(x.Narration), Href: bankHref(x)})
			}
		}
	}
	a.bankRender(w, "bank_tx", http.StatusOK, s, roomPage{"Title": Normalise(l.Narration), "Nav": "accounts", "A": acc, "L": bankLineView{FeedLine: l, Description: Normalise(l.Narration)}, "Related": related})
}

func (a *bankSite) transferPage(ctx context.Context, s *bankSession, data roomPage) (roomPage, error) {
	_, accts, err := a.myAccounts(ctx, s.Customer)
	if err != nil {
		return nil, err
	}
	data["Title"] = "Send money"
	data["Nav"] = "transfer"
	data["Accounts"] = accts
	a.mu.Lock()
	data["Banks"] = a.openInstitutions()
	a.mu.Unlock()
	return data, nil
}

func (a *bankSite) obTransferForm(w http.ResponseWriter, r *http.Request) {
	s := a.bankUser(w, r)
	if s == nil {
		return
	}
	data, err := a.transferPage(r.Context(), s, roomPage{"Form": url.Values{"from": {r.URL.Query().Get("from")}, "bank": {a.bank.ID}}})
	if err != nil {
		a.bankFail(w, r, err)
		return
	}
	a.bankRender(w, "bank_transfer", http.StatusOK, s, data)
}

func (a *bankSite) obTransferRequest(ctx context.Context, s *bankSession, f url.Values) (TransferRequest, error) {
	_, acc, ok, err := a.myAccount(ctx, s.Customer, f.Get("from"))
	if err != nil {
		return TransferRequest{}, err
	}
	if !ok {
		return TransferRequest{}, errChooseAccount
	}
	amount, err := parseNaira(f.Get("amount"))
	if err != nil {
		return TransferRequest{}, errBadAmount
	}
	req := TransferRequest{FromBank: a.bank.ID, From: acc.NUBAN, Amount: amount, Narration: strings.TrimSpace(f.Get("narration"))}
	if f.Get("mode") == "own" {
		req.ToBank, req.To = a.bank.ID, f.Get("own")
		if req.To == "" {
			return req, errChooseOwn
		}
		return req, nil
	}
	req.ToBank, req.To = f.Get("bank"), strings.TrimSpace(f.Get("number"))
	if len(req.To) != 10 {
		return req, errTenDigits
	}
	return req, nil
}

var (
	errChooseAccount = errors.New("choose one of your accounts to pay from")
	errChooseOwn     = errors.New("choose which of your accounts to pay into")
	errTenDigits     = errors.New("enter a 10-digit account number")
)

func userFixable(err error) bool {
	var te *transferError
	return errors.As(err, &te) || errors.Is(err, errBadAmount) || errors.Is(err, errChooseAccount) || errors.Is(err, errChooseOwn) || errors.Is(err, errTenDigits)
}

func (a *bankSite) transferFailed(w http.ResponseWriter, r *http.Request, s *bankSession, err error) {
	if !userFixable(err) {
		a.bankFail(w, r, err)
		return
	}
	data, perr := a.transferPage(r.Context(), s, roomPage{"Error": sentence(err.Error()), "Form": r.PostForm})
	if perr != nil {
		a.bankFail(w, r, perr)
		return
	}
	a.bankRender(w, "bank_transfer", http.StatusUnprocessableEntity, s, data)
}

func (a *bankSite) obTransferReview(w http.ResponseWriter, r *http.Request) {
	s := a.bankUser(w, r)
	if s == nil {
		return
	}
	req, err := a.obTransferRequest(r.Context(), s, r.PostForm)
	var q TransferResult
	if err == nil {
		q, err = a.Quote(req)
	}
	if err != nil {
		a.transferFailed(w, r, s, err)
		return
	}
	a.mu.Lock()
	to := q.ToBank
	if b := a.bankLocked(q.ToBank); b != nil {
		to = b.Name
	}
	a.mu.Unlock()
	a.bankRender(w, "bank_review", http.StatusOK, s, roomPage{"Title": "Review transfer", "Nav": "transfer", "Q": q, "ToBank": to, "Form": r.PostForm})
}

func (a *bankSite) obTransferConfirm(w http.ResponseWriter, r *http.Request) {
	s := a.bankUser(w, r)
	if s == nil {
		return
	}
	req, err := a.obTransferRequest(r.Context(), s, r.PostForm)
	var res TransferResult
	if err == nil && a.role == RoleBank {
		res, err = a.remoteTransfer(req)
	} else if err == nil {
		res, err = a.Transfer(req)
	}
	if err != nil {
		a.transferFailed(w, r, s, err)
		return
	}
	a.SyncAll(r.Context())
	http.Redirect(w, r, "/accounts/"+res.From+"?transfer="+url.QueryEscape(res.Status), http.StatusSeeOther)
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

const (
	historyPageSize = 25
	maxHistoryPages = 1000
)

func (a *bankSite) obApps(w http.ResponseWriter, r *http.Request) {
	s := a.bankUser(w, r)
	if s == nil {
		return
	}
	page, ok := pageParam(w, r, "access history", maxHistoryPages)
	if !ok {
		return
	}
	need := page*historyPageSize + 1
	ctx := r.Context()
	consents, err := a.ob.Consents(ctx, s.Customer)
	if err != nil {
		a.bankFail(w, r, err)
		return
	}
	access, err := a.ob.AccessHistory(ctx, s.Customer, need)
	if err != nil {
		a.bankFail(w, r, err)
		return
	}
	_, accts, err := a.myAccounts(ctx, s.Customer)
	if err != nil {
		a.bankFail(w, r, err)
		return
	}
	byNUBAN := map[string]string{}
	for _, acc := range accts {
		byNUBAN[acc.NUBAN] = strings.Title(acc.Type) + " · " + acc.NUBAN
	}
	rows := []appRow{}
	for _, c := range consents {
		var status string
		switch c.Status {
		case openbanking.Authorised:
			status = "active"
		case openbanking.Revoked:
			status = "revoked"
		case openbanking.Expired:
			status = "expired"
		case openbanking.Dormant:
			status = "dormant"
		case openbanking.ReconsentRequired:
			status = "renew"
		default:
			continue
		}
		row := appRow{ID: c.ID, Partner: c.PartnerName, Status: status, Since: c.AuthorisedAt, Until: c.ExpiresAt, LastAccess: c.LastUsedAt}
		if row.Partner == "" {
			row.Partner = c.ClientID
		}
		for _, n := range c.Accounts {
			row.Accounts = append(row.Accounts, byNUBAN[n])
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Since.After(rows[j].Since) })
	history := []accessRow{}
	names := map[string]string{}
	for _, c := range consents {
		names[c.ID] = c.PartnerName
		if c.PartnerName == "" {
			names[c.ID] = c.ClientID
		}
	}
	for _, e := range access {
		history = append(history, accessRow{Partner: names[e.ConsentID], Account: byNUBAN[e.Account], At: e.At})
	}
	sort.SliceStable(history, func(i, j int) bool { return history[i].At.After(history[j].At) })
	if len(history) < need {
		if _, ok := pageParam(w, r, "access history", pagesOf(len(history), historyPageSize)); !ok {
			return
		}
	}
	start := min((page-1)*historyPageSize, len(history))
	end := min(page*historyPageSize, len(history))
	older := len(history) > end
	history = history[start:end]
	a.mu.Lock()
	via := a.cfg.Connect.BrandName
	a.mu.Unlock()
	a.bankRender(w, "bank_apps", http.StatusOK, s, roomPage{"Title": "Connected apps", "Nav": "apps", "Apps": rows, "History": history, "Page": page, "Older": older, "Newer": page > 1, "Revoked": r.URL.Query().Get("revoked") == "1", "Via": via})
}

func (a *bankSite) obRevoke(w http.ResponseWriter, r *http.Request) {
	s := a.bankUser(w, r)
	if s == nil {
		return
	}
	if err := a.ob.Revoke(r.Context(), r.PathValue("id"), s.Customer, "bank"); err != nil {
		http.NotFound(w, r)
		return
	}
	a.flushEvents()
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
