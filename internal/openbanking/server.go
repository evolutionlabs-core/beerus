package openbanking

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

const (
	assertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
	requestPrefix = "urn:ietf:params:oauth:request_uri:"
	maxForm       = 64 << 10
)

type Config struct {
	BankID        string
	BankName      string
	Issuer        string
	AuthorizeURL  string
	HistoryDays   int
	Store         Store
	Registry      Registry
	Data          Data
	Ledger        Ledger
	Now           func() time.Time
	Biz           func() time.Time
	CodeLife      time.Duration
	AccessLife    time.Duration
	RefreshGrace  time.Duration
	RequestLife   time.Duration
	AssertionLife time.Duration
	ConsentMonths int
	RefreshDays   int
	DormantDays   int
}

const (
	DefaultConsentMonths = 12
	DefaultRefreshDays   = 372
	DefaultDormantDays   = 180
	DefaultAssertionLife = 5 * time.Minute
)

type Server struct {
	cfg   Config
	mu    sync.Mutex
	pars  map[string]*par
	codes map[string]*code
	jtis  map[string]time.Time
}

type par struct {
	clientID  string
	redirect  string
	challenge string
	state     string
	consentID string
	expires   time.Time
	used      bool
}

type code struct {
	clientID  string
	redirect  string
	challenge string
	consentID string
	expires   time.Time
	used      bool
}

var (
	ErrInvalidRequest = errors.New("The request is invalid or has expired.")
	ErrNoAccounts     = errors.New("Choose at least one of your accounts.")
	ErrNotYours       = errors.New("That consent does not belong to you.")
)

func New(cfg Config) *Server {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Biz == nil {
		cfg.Biz = cfg.Now
	}
	if cfg.CodeLife <= 0 || cfg.CodeLife > time.Minute {
		cfg.CodeLife = time.Minute
	}
	if cfg.RequestLife <= 0 || cfg.RequestLife > 10*time.Minute {
		cfg.RequestLife = 10 * time.Minute
	}
	if cfg.AccessLife <= 0 {
		cfg.AccessLife = 10 * time.Minute
	}
	if cfg.RefreshGrace <= 0 {
		cfg.RefreshGrace = time.Minute
	}
	if cfg.AssertionLife <= 0 {
		cfg.AssertionLife = DefaultAssertionLife
	}
	if cfg.ConsentMonths <= 0 {
		cfg.ConsentMonths = DefaultConsentMonths
	}
	if cfg.RefreshDays <= 0 {
		cfg.RefreshDays = DefaultRefreshDays
	}
	if cfg.DormantDays <= 0 {
		cfg.DormantDays = DefaultDormantDays
	}
	return &Server{cfg: cfg, pars: map[string]*par{}, codes: map[string]*code{}, jtis: map[string]time.Time{}}
}

func Hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func Thumbprint(der []byte) string {
	sum := sha256.Sum256(der)
	return b64.EncodeToString(sum[:])
}

func random(prefix string, n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + b64.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status int, code, desc string) {
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer error="`+code+`"`)
	}
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

func (s *Server) Data() Data {
	return s.cfg.Data
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /discover", s.discover)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.discover)
	mux.HandleFunc("POST /par", s.par)
	mux.HandleFunc("POST /token", s.token)
	mux.HandleFunc("GET /accounts", s.resource("accounts", s.listAccounts))
	mux.HandleFunc("GET /accounts/{id}", s.resource("accounts", s.getAccount))
	mux.HandleFunc("GET /accounts/{id}/balances", s.resource("balances", s.getBalances))
	mux.HandleFunc("GET /accounts/{id}/transactions", s.resource("transactions", s.getTransactions))
	return mux
}

func (s *Server) caller(w http.ResponseWriter, r *http.Request) (*Client, string, bool) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		fail(w, http.StatusUnauthorized, "invalid_client", "A client certificate is required.")
		return nil, "", false
	}
	cert := r.TLS.PeerCertificates[0]
	c, ok := s.cfg.Registry.ClientByCert(cert.Subject.CommonName)
	if !ok {
		fail(w, http.StatusUnauthorized, "invalid_client", "The certificate is not registered at the Registry.")
		return nil, "", false
	}
	if c.Role == "" || !slices.Contains(cert.Subject.OrganizationalUnit, c.Role) {
		fail(w, http.StatusForbidden, "forbidden", "The certificate does not carry the role this client is registered with.")
		return nil, "", false
	}
	return c, Thumbprint(cert.Raw), true
}

func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (*Client, string, bool) {
	c, thumb, ok := s.caller(w, r)
	if !ok {
		return nil, "", false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxForm)
	if err := r.ParseForm(); err != nil {
		fail(w, http.StatusBadRequest, "invalid_request", "The body must be a form.")
		return nil, "", false
	}
	if id := r.PostForm.Get("client_id"); id != "" && id != c.ID {
		fail(w, http.StatusUnauthorized, "invalid_client", "The client_id does not match the certificate.")
		return nil, "", false
	}
	if r.PostForm.Get("client_assertion_type") != assertionType {
		fail(w, http.StatusUnauthorized, "invalid_client", "Authenticate with private_key_jwt.")
		return nil, "", false
	}
	claims, err := Verify(r.PostForm.Get("client_assertion"), c.Keys)
	if err != nil {
		fail(w, http.StatusUnauthorized, "invalid_client", "The client assertion is not valid: "+err.Error()+".")
		return nil, "", false
	}
	now := s.cfg.Now()
	exp, _ := claims["exp"].(float64)
	jti, _ := claims["jti"].(string)
	if claims["iss"] != c.ID || claims["sub"] != c.ID || !s.audience(claims["aud"]) || jti == "" ||
		time.Unix(int64(exp), 0).Before(now) || time.Unix(int64(exp), 0).After(now.Add(s.cfg.AssertionLife)) {
		fail(w, http.StatusUnauthorized, "invalid_client", "The client assertion has the wrong issuer, audience or lifetime.")
		return nil, "", false
	}
	s.mu.Lock()
	for k, until := range s.jtis {
		if until.Before(now) {
			delete(s.jtis, k)
		}
	}
	_, seen := s.jtis[c.ID+"|"+jti]
	s.jtis[c.ID+"|"+jti] = time.Unix(int64(exp), 0)
	s.mu.Unlock()
	if seen {
		fail(w, http.StatusUnauthorized, "invalid_client", "The client assertion was already used.")
		return nil, "", false
	}
	return c, thumb, true
}

func (s *Server) audience(aud any) bool {
	ok := func(v any) bool {
		x, _ := v.(string)
		return x == s.cfg.Issuer
	}
	if list, isList := aud.([]any); isList {
		return slices.ContainsFunc(list, ok)
	}
	return ok(aud)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.caller(w, r); !ok {
		return
	}
	if err := s.cfg.Data.Ping(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "down"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "bank": s.cfg.BankID})
}

func (s *Server) discover(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.caller(w, r); !ok {
		return
	}
	i := s.cfg.Issuer
	writeJSON(w, http.StatusOK, map[string]any{
		"bank_id":                                          s.cfg.BankID,
		"name":                                             s.cfg.BankName,
		"issuer":                                           i,
		"authorization_endpoint":                           s.cfg.AuthorizeURL,
		"pushed_authorization_request_endpoint":            i + "/par",
		"require_pushed_authorization_requests":            true,
		"token_endpoint":                                   i + "/token",
		"accounts_endpoint":                                i + "/accounts",
		"response_types_supported":                         []string{"code"},
		"grant_types_supported":                            []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":                 []string{"S256"},
		"token_endpoint_auth_methods_supported":            []string{"private_key_jwt"},
		"token_endpoint_auth_signing_alg_values_supported": []string{"ES256"},
		"tls_client_certificate_bound_access_tokens":       true,
		"authorization_response_iss_parameter_supported":   true,
		"scopes_supported":                                 Scopes,
		"history_days":                                     s.cfg.HistoryDays,
	})
}

func (s *Server) par(w http.ResponseWriter, r *http.Request) {
	c, _, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	s.sweep(r.Context())
	f := r.PostForm
	if f.Get("response_type") != "code" || f.Get("code_challenge_method") != "S256" || len(f.Get("code_challenge")) != 43 {
		fail(w, http.StatusBadRequest, "invalid_request", "Use response_type=code with a PKCE S256 challenge.")
		return
	}
	if f.Get("request_uri") != "" {
		fail(w, http.StatusBadRequest, "invalid_request", "request_uri is not allowed in a pushed request.")
		return
	}
	redirect := f.Get("redirect_uri")
	if !slices.Contains(c.RedirectURIs, redirect) {
		fail(w, http.StatusBadRequest, "invalid_request", "The redirect_uri is not registered for this client.")
		return
	}
	scopes, err := parseScope(f.Get("scope"))
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid_scope", err.Error())
		return
	}
	from := clock.DateOf(s.cfg.Biz())
	if v := f.Get("transaction_from"); v != "" {
		d, err := clock.ParseDate(v)
		if err != nil {
			fail(w, http.StatusBadRequest, "invalid_request", "transaction_from must be a date in YYYY-MM-DD form.")
			return
		}
		from = d
	}
	now := s.cfg.Now()
	consent := &Consent{
		ID:              random("obc_", 12),
		ClientID:        c.ID,
		PartnerID:       f.Get("partner_id"),
		PartnerName:     f.Get("partner_name"),
		PartnerLegal:    f.Get("partner_legal_name"),
		Purpose:         f.Get("purpose"),
		Status:          AwaitingAuthorisation,
		Scopes:          scopes,
		TransactionFrom: from,
		Analytics:       f.Get("analytics") == "yes",
		CreatedAt:       s.cfg.Biz(),
	}
	if err := s.cfg.Store.CreateConsent(r.Context(), consent); err != nil {
		fail(w, http.StatusInternalServerError, "server_error", "Something went wrong.")
		return
	}
	uri := random(requestPrefix, 24)
	s.mu.Lock()
	s.pars[uri] = &par{clientID: c.ID, redirect: redirect, challenge: f.Get("code_challenge"), state: f.Get("state"), consentID: consent.ID, expires: now.Add(s.cfg.RequestLife)}
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{"request_uri": uri, "expires_in": int(s.cfg.RequestLife.Seconds()), "consent_id": consent.ID})
}

func parseScope(raw string) ([]string, error) {
	out := []string{}
	for _, v := range strings.Fields(raw) {
		if !slices.Contains(Scopes, v) {
			return nil, fmt.Errorf("The scope %q is not supported.", v)
		}
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("Ask for at least one scope.")
	}
	sort.Strings(out)
	return out, nil
}

type Request struct {
	Client    *Client
	Consent   *Consent
	Effective clock.Date
	Capped    bool
	Months    int
	Until     time.Time
}

func (s *Server) renewal(from time.Time) time.Time {
	return addMonths(from, s.cfg.ConsentMonths)
}

func addMonths(t time.Time, n int) time.Time {
	y, m, d := t.Date()
	first := time.Date(y, m+time.Month(n), 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
	if last := first.AddDate(0, 1, -1).Day(); d > last {
		d = last
	}
	return first.AddDate(0, 0, d-1)
}

func (s *Server) sweep(ctx context.Context) {
	now := s.cfg.Now()
	stale := []string{}
	s.mu.Lock()
	for uri, p := range s.pars {
		switch {
		case p.used:
			delete(s.pars, uri)
		case now.After(p.expires):
			stale = append(stale, p.consentID)
			delete(s.pars, uri)
		}
	}
	for h, cd := range s.codes {
		if now.After(cd.expires.Add(s.cfg.RequestLife)) {
			delete(s.codes, h)
		}
	}
	s.mu.Unlock()
	for _, id := range stale {
		if c, err := s.cfg.Store.Consent(ctx, id); err == nil {
			_ = s.expireAwaiting(ctx, c)
		}
	}
}

func (s *Server) expireAwaiting(ctx context.Context, c *Consent) error {
	if c.Status != AwaitingAuthorisation {
		return nil
	}
	c.Status = Expired
	if err := s.cfg.Store.UpdateConsent(ctx, c); err != nil {
		return err
	}
	s.record(c, Expired, "bank")
	return nil
}

func (s *Server) Resume(ctx context.Context) error {
	list, err := s.cfg.Store.Awaiting(ctx)
	if err != nil {
		return err
	}
	for _, c := range list {
		if err := s.expireAwaiting(ctx, c); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) HistoryFloor() clock.Date {
	return clock.DateOf(s.cfg.Biz()).AddDays(-s.cfg.HistoryDays)
}

func (s *Server) Request(ctx context.Context, requestURI, clientID string) (*Request, error) {
	s.mu.Lock()
	p := s.pars[requestURI]
	valid := p != nil && !p.used && !s.cfg.Now().After(p.expires) && p.clientID == clientID
	s.mu.Unlock()
	if !valid {
		return nil, ErrInvalidRequest
	}
	client, ok := s.cfg.Registry.Client(p.clientID)
	if !ok {
		return nil, ErrInvalidRequest
	}
	c, err := s.cfg.Store.Consent(ctx, p.consentID)
	if err != nil || c.Status != AwaitingAuthorisation {
		return nil, ErrInvalidRequest
	}
	eff, capped := c.TransactionFrom, false
	if floor := s.HistoryFloor(); eff.Before(floor) {
		eff, capped = floor, true
	}
	return &Request{Client: client, Consent: c, Effective: eff, Capped: capped, Months: s.cfg.ConsentMonths, Until: s.renewal(s.cfg.Biz())}, nil
}

func (s *Server) take(requestURI string) (*par, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pars[requestURI]
	if p == nil || p.used || s.cfg.Now().After(p.expires) {
		return nil, ErrInvalidRequest
	}
	p.used = true
	delete(s.pars, requestURI)
	return p, nil
}

func (s *Server) redirect(base string, q url.Values) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	v := u.Query()
	for k, vs := range q {
		v[k] = vs
	}
	v.Set("iss", s.cfg.Issuer)
	u.RawQuery = v.Encode()
	return u.String()
}

func (s *Server) record(c *Consent, status Status, via string) {
	if s.cfg.Ledger != nil {
		s.cfg.Ledger.Record(ConsentEvent{ConsentID: c.ID, Bank: s.cfg.BankID, ClientID: c.ClientID, PartnerID: c.PartnerID, Status: status, Via: via, At: s.cfg.Biz()})
	}
}

func (s *Server) Approve(ctx context.Context, requestURI, customer string, accounts []string, analytics bool) (string, error) {
	s.mu.Lock()
	p := s.pars[requestURI]
	ok := p != nil && !p.used && !s.cfg.Now().After(p.expires)
	s.mu.Unlock()
	if !ok {
		return "", ErrInvalidRequest
	}
	mine, err := s.cfg.Data.Accounts(customer)
	if err != nil {
		return "", err
	}
	chosen := []string{}
	for _, a := range accounts {
		if slices.ContainsFunc(mine, func(x AccountInfo) bool { return x.ID == a }) && !slices.Contains(chosen, a) {
			chosen = append(chosen, a)
		}
	}
	if len(chosen) == 0 {
		return "", ErrNoAccounts
	}
	slices.Sort(chosen)
	c, err := s.cfg.Store.Consent(ctx, p.consentID)
	if err != nil || c.Status != AwaitingAuthorisation {
		return "", ErrInvalidRequest
	}
	if _, err := s.take(requestURI); err != nil {
		return "", err
	}
	now := s.cfg.Biz()
	if floor := s.HistoryFloor(); c.TransactionFrom.Before(floor) {
		c.TransactionFrom = floor
	}
	c.Customer, c.Accounts, c.Analytics = customer, chosen, analytics
	c.Status, c.AuthorisedAt, c.ExpiresAt = Authorised, now, s.renewal(now)
	if err := s.cfg.Store.UpdateConsent(ctx, c); err != nil {
		return "", err
	}
	s.record(c, Authorised, "bank")
	raw := random("oc_", 24)
	s.mu.Lock()
	s.codes[Hash(raw)] = &code{clientID: p.clientID, redirect: p.redirect, challenge: p.challenge, consentID: c.ID, expires: s.cfg.Now().Add(s.cfg.CodeLife)}
	s.mu.Unlock()
	q := url.Values{"code": {raw}}
	if p.state != "" {
		q.Set("state", p.state)
	}
	return s.redirect(p.redirect, q), nil
}

func (s *Server) Reject(ctx context.Context, requestURI string) (string, error) {
	p, err := s.take(requestURI)
	if err != nil {
		return "", err
	}
	if c, err := s.cfg.Store.Consent(ctx, p.consentID); err == nil && c.Status == AwaitingAuthorisation {
		c.Status = Rejected
		if err := s.cfg.Store.UpdateConsent(ctx, c); err == nil {
			s.record(c, Rejected, "bank")
		}
	}
	q := url.Values{"error": {"access_denied"}, "error_description": {"The customer declined."}}
	if p.state != "" {
		q.Set("state", p.state)
	}
	return s.redirect(p.redirect, q), nil
}

func (s *Server) Revoke(ctx context.Context, consentID, customer, via string) error {
	c, err := s.cfg.Store.Consent(ctx, consentID)
	if err != nil {
		return err
	}
	if customer == "" || c.Customer != customer {
		return ErrNotYours
	}
	return s.revoke(ctx, c, via)
}

func (s *Server) revoke(ctx context.Context, c *Consent, via string) error {
	if c.Status != Authorised && c.Status != AwaitingAuthorisation {
		return nil
	}
	c.Status, c.RevokedVia = Revoked, via
	if err := s.cfg.Store.UpdateConsent(ctx, c); err != nil {
		return err
	}
	if err := s.cfg.Store.RevokeConsentTokens(ctx, c.ID); err != nil {
		return err
	}
	s.record(c, Revoked, via)
	return nil
}

func (s *Server) Effective(c *Consent) Status {
	if c.Status != Authorised {
		return c.Status
	}
	now := s.cfg.Biz()
	if !now.Before(c.ExpiresAt) {
		return ReconsentRequired
	}
	lastUse := c.LastUsedAt
	if lastUse.Before(c.AuthorisedAt) {
		lastUse = c.AuthorisedAt
	}
	if !now.Before(lastUse.AddDate(0, 0, s.cfg.DormantDays)) {
		return Dormant
	}
	return Authorised
}

func (s *Server) Consents(ctx context.Context, customer string) ([]*Consent, error) {
	list, err := s.cfg.Store.ConsentsFor(ctx, customer)
	for _, c := range list {
		c.Status = s.Effective(c)
	}
	return list, err
}

func (s *Server) Consent(ctx context.Context, id string) (*Consent, error) {
	c, err := s.cfg.Store.Consent(ctx, id)
	if err == nil {
		c.Status = s.Effective(c)
	}
	return c, err
}

func (s *Server) AccessHistory(ctx context.Context, customer string, limit int) ([]Access, error) {
	return s.cfg.Store.AccessFor(ctx, customer, limit)
}

var consentCodes = map[Status]string{
	Revoked:               "consent_revoked",
	Expired:               "consent_expired",
	Dormant:               "consent_dormant",
	ReconsentRequired:     "consent_reconsent_required",
	Rejected:              "consent_not_authorised",
	AwaitingAuthorisation: "consent_not_authorised",
}

type consentError struct {
	code string
}

func (e consentError) Error() string {
	return e.code
}

func (s *Server) live(ctx context.Context, id string) (*Consent, error) {
	c, err := s.cfg.Store.Consent(ctx, id)
	if err != nil {
		return nil, consentError{"consent_not_authorised"}
	}
	if eff := s.Effective(c); eff != c.Status {
		c.Status = eff
		if err := s.cfg.Store.UpdateConsent(ctx, c); err != nil {
			return nil, err
		}
		if err := s.cfg.Store.RevokeConsentTokens(ctx, c.ID); err != nil {
			return nil, err
		}
		s.record(c, eff, "bank")
	}
	if c.Status != Authorised {
		return nil, consentError{consentCodes[c.Status]}
	}
	return c, nil
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	s.sweep(r.Context())
	c, thumb, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		s.codeGrant(w, r, c, thumb)
	case "refresh_token":
		s.refreshGrant(w, r, c, thumb)
	default:
		fail(w, http.StatusBadRequest, "unsupported_grant_type", "Use authorization_code or refresh_token.")
	}
}

func (s *Server) codeGrant(w http.ResponseWriter, r *http.Request, client *Client, thumb string) {
	f := r.PostForm
	h := Hash(f.Get("code"))
	s.mu.Lock()
	cd := s.codes[h]
	reused := cd != nil && cd.used
	if cd != nil {
		cd.used = true
	}
	s.mu.Unlock()
	if cd == nil {
		fail(w, http.StatusBadRequest, "invalid_grant", "The code is unknown.")
		return
	}
	if reused {
		_ = s.cfg.Store.RevokeChildren(r.Context(), "code:"+h)
		fail(w, http.StatusBadRequest, "invalid_grant", "The code was already used.")
		return
	}
	verifier := f.Get("code_verifier")
	sum := sha256.Sum256([]byte(verifier))
	switch {
	case cd.clientID != client.ID:
		fail(w, http.StatusBadRequest, "invalid_grant", "The code was issued to another client.")
	case s.cfg.Now().After(cd.expires):
		fail(w, http.StatusBadRequest, "invalid_grant", "The code has expired.")
	case f.Get("redirect_uri") != cd.redirect:
		fail(w, http.StatusBadRequest, "invalid_grant", "The redirect_uri does not match the request.")
	case len(verifier) < 43 || len(verifier) > 128 || subtle.ConstantTimeCompare([]byte(b64.EncodeToString(sum[:])), []byte(cd.challenge)) != 1:
		fail(w, http.StatusBadRequest, "invalid_grant", "The PKCE verifier does not match.")
	default:
		consent, err := s.live(r.Context(), cd.consentID)
		if err != nil {
			fail(w, http.StatusBadRequest, "invalid_grant", "The consent is not active.")
			return
		}
		s.issue(w, r, consent, client.ID, thumb, "code:"+h, random("fam_", 9))
	}
}

func (s *Server) refreshGrant(w http.ResponseWriter, r *http.Request, client *Client, thumb string) {
	ctx := r.Context()
	h := Hash(r.PostForm.Get("refresh_token"))
	tok, err := s.cfg.Store.Token(ctx, h)
	now := s.cfg.Now()
	if err != nil || tok.Kind != "refresh" || tok.Revoked || tok.ClientID != client.ID || now.After(tok.ExpiresAt) {
		fail(w, http.StatusBadRequest, "invalid_grant", "The refresh token is not valid.")
		return
	}
	if subtle.ConstantTimeCompare([]byte(tok.Thumbprint), []byte(thumb)) != 1 {
		fail(w, http.StatusBadRequest, "invalid_grant", "The refresh token is bound to another certificate.")
		return
	}
	consent, err := s.live(ctx, tok.ConsentID)
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid_grant", "The consent is not active.")
		return
	}
	first, err := s.cfg.Store.MarkRotated(ctx, h, now)
	if err != nil {
		fail(w, http.StatusInternalServerError, "server_error", "Something went wrong.")
		return
	}
	if !first {
		cur, err := s.cfg.Store.Token(ctx, h)
		if err != nil || now.Sub(cur.RotatedAt) > s.cfg.RefreshGrace {
			_ = s.cfg.Store.RevokeFamily(ctx, tok.Family)
			fail(w, http.StatusBadRequest, "invalid_grant", "The refresh token was already used.")
			return
		}
		_ = s.cfg.Store.RevokeChildren(ctx, h)
	}
	s.issue(w, r, consent, client.ID, thumb, h, tok.Family)
}

func (s *Server) issue(w http.ResponseWriter, r *http.Request, c *Consent, clientID, thumb, parent, family string) {
	now := s.cfg.Now()
	access, refresh := random("oba_", 24), random("obr_", 24)
	for _, t := range []*Token{
		{Hash: Hash(access), Kind: "access", ExpiresAt: now.Add(s.cfg.AccessLife)},
		{Hash: Hash(refresh), Kind: "refresh", ExpiresAt: now.AddDate(0, 0, s.cfg.RefreshDays)},
	} {
		t.ConsentID, t.ClientID, t.Thumbprint, t.Family, t.Parent, t.CreatedAt = c.ID, clientID, thumb, family, parent, now
		if err := s.cfg.Store.PutToken(r.Context(), t); err != nil {
			fail(w, http.StatusInternalServerError, "server_error", "Something went wrong.")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  access,
		"token_type":    "Bearer",
		"expires_in":    int(s.cfg.AccessLife.Seconds()),
		"refresh_token": refresh,
		"scope":         strings.Join(c.Scopes, " "),
		"consent_id":    c.ID,
	})
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}
