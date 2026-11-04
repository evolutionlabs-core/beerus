package openbanking

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/pki"
)

type fakeRegistry struct {
	client *Client
}

func (r fakeRegistry) Client(id string) (*Client, bool) {
	return r.client, id == r.client.ID
}

func (r fakeRegistry) ClientByCert(cn string) (*Client, bool) {
	return r.client, cn == r.client.CertCN
}

type fakeData struct{}

func (fakeData) Accounts(customer string) ([]AccountInfo, error) {
	return []AccountInfo{{ID: "0123456789", Name: "Current", Type: "current", Currency: "NGN"}, {ID: "0123456790", Name: "Savings", Type: "savings", Currency: "NGN"}}, nil
}

func (fakeData) Account(id string) (AccountInfo, error) {
	return AccountInfo{ID: id, Name: "Current", Type: "current", Currency: "NGN"}, nil
}

func (fakeData) Balance(string) (Balance, error) {
	return Balance{Ledger: 5000, Available: 4000, Currency: "NGN"}, nil
}

func (fakeData) BalanceOn(string, clock.Date) (int64, error) {
	return 4200, nil
}

func (fakeData) Transactions(string, clock.Date, clock.Date) ([]Txn, error) {
	d, _ := clock.ParseDate("2026-01-05")
	out := []Txn{}
	for i := 0; i < 5; i++ {
		out = append(out, Txn{ID: string(rune('a' + i)), Amount: int64(i), ValueDate: d, Seq: int64(i), BookedAt: time.Now()})
	}
	return out, nil
}

func (fakeData) Ping() error {
	return nil
}

type recorder struct {
	mu     sync.Mutex
	events []ConsentEvent
}

func (r *recorder) Record(e ConsentEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

type env struct {
	t      *testing.T
	srv    *Server
	ts     *httptest.Server
	store  *MemStore
	key    *ecdsa.PrivateKey
	ca     *pki.Authority
	good   *http.Client
	other  *http.Client
	now    time.Time
	biz    time.Time
	ledger *recorder
	mu     sync.Mutex
}

func (e *env) clientFor(cn, role string) *http.Client {
	leaf, err := e.ca.IssueClient(cn, role, time.Hour)
	if err != nil {
		e.t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(leaf.CertPEM, leaf.KeyPEM)
	if err != nil {
		e.t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(e.ca.PEM)
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{pair}, RootCAs: pool}}}
}

func newEnv(t *testing.T, tweaks ...func(*Config)) *env {
	t.Helper()
	ca, err := pki.NewAuthority("Test CA", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	srvLeaf, err := ca.IssueServer([]string{"localhost"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(srvLeaf.CertPEM, srvLeaf.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.PEM)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, store: NewMemStore(), key: key, ca: ca, now: time.Now(), biz: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC), ledger: &recorder{}}
	var h http.Handler
	e.ts = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) }))
	e.ts.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}
	e.ts.StartTLS()
	t.Cleanup(e.ts.Close)
	client := &Client{ID: "OBR-1", Name: "Connect", Role: "aggregator", CertCN: "connect", Keys: []*ecdsa.PublicKey{&key.PublicKey}, RedirectURIs: []string{"https://connect.localhost/cb"}}
	cfg := Config{
		BankID: "bank-a", BankName: "Bank A", Issuer: e.ts.URL, AuthorizeURL: "http://bank-a.localhost/authorize", HistoryDays: 90,
		Store: e.store, Registry: fakeRegistry{client}, Data: fakeData{}, Ledger: e.ledger,
		Now: func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now },
		Biz: func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.biz },
	}
	for _, tweak := range tweaks {
		tweak(&cfg)
	}
	e.srv = New(cfg)
	h = e.srv.Handler()
	e.good = e.clientFor("connect", "aggregator")
	e.other = e.clientFor("connect", "aggregator")
	return e
}

func (e *env) advance(real, biz time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.now = e.now.Add(real)
	e.biz = e.biz.Add(biz)
}

func (e *env) assertion() string {
	e.t.Helper()
	jwt, err := Sign(e.key, "k1", map[string]any{"iss": "OBR-1", "sub": "OBR-1", "aud": e.ts.URL, "jti": random("j", 8), "exp": e.now.Add(time.Minute).Unix(), "iat": e.now.Unix()})
	if err != nil {
		e.t.Fatal(err)
	}
	return jwt
}

func (e *env) post(c *http.Client, path string, f url.Values) (int, map[string]any) {
	e.t.Helper()
	if f.Get("client_assertion") == "" {
		f.Set("client_assertion", e.assertion())
		f.Set("client_assertion_type", assertionType)
	}
	resp, err := c.PostForm(e.ts.URL+path, f)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (e *env) get(c *http.Client, path, token string) (int, map[string]any) {
	e.t.Helper()
	req, _ := http.NewRequest("GET", e.ts.URL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"

func challenge(v string) string {
	sum := sha256.Sum256([]byte(v))
	return b64.EncodeToString(sum[:])
}

const allScopes = "accounts balances transactions"

func (e *env) pushed() (string, string) {
	return e.pushedFor(allScopes)
}

func (e *env) pushedFor(scope string) (string, string) {
	e.t.Helper()
	status, body := e.post(e.good, "/par", url.Values{
		"response_type": {"code"}, "client_id": {"OBR-1"}, "redirect_uri": {"https://connect.localhost/cb"}, "state": {"st1"},
		"code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}, "partner_id": {"balance"}, "partner_name": {"Balance"},
		"transaction_from": {"2026-02-01"}, "scope": {scope},
	})
	if status != http.StatusCreated {
		e.t.Fatalf("par: %d %v", status, body)
	}
	return body["request_uri"].(string), body["consent_id"].(string)
}

func (e *env) code() (string, string) {
	return e.codeFor(allScopes)
}

func (e *env) codeFor(scope string) (string, string) {
	e.t.Helper()
	uri, id := e.pushedFor(scope)
	loc, err := e.srv.Approve(context.Background(), uri, "adaeze", []string{"0123456789"}, false)
	if err != nil {
		e.t.Fatal(err)
	}
	u, _ := url.Parse(loc)
	if u.Query().Get("iss") != e.ts.URL || u.Query().Get("state") != "st1" {
		e.t.Fatalf("redirect %s lacks iss or state", loc)
	}
	return u.Query().Get("code"), id
}

func (e *env) exchange(c *http.Client, code, v string) (int, map[string]any) {
	return e.post(c, "/token", url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {v}, "redirect_uri": {"https://connect.localhost/cb"}})
}

func (e *env) tokens() (map[string]any, string) {
	return e.tokensFor(allScopes)
}

func (e *env) tokensFor(scope string) (map[string]any, string) {
	e.t.Helper()
	code, id := e.codeFor(scope)
	status, body := e.exchange(e.good, code, verifier)
	if status != http.StatusOK {
		e.t.Fatalf("token: %d %v", status, body)
	}
	return body, id
}

func TestFullFlow(t *testing.T) {
	e := newEnv(t)
	tok, id := e.tokens()
	access := tok["access_token"].(string)
	if status, body := e.get(e.good, "/accounts", access); status != 200 || len(body["data"].([]any)) != 1 {
		t.Fatalf("accounts: %d %v", status, body)
	}
	if status, body := e.get(e.good, "/accounts/0123456789/balances", access); status != 200 || body["ledger_balance"].(map[string]any)["amount_minor"].(float64) != 5000 {
		t.Fatalf("balances: %d %v", status, body)
	}
	status, body := e.get(e.good, "/accounts/0123456789/transactions?page_size=2", access)
	if status != 200 || len(body["data"].([]any)) != 2 || body["next_cursor"] != "2026-01-05.1" {
		t.Fatalf("transactions: %d %v", status, body)
	}
	if status, _ := e.get(e.good, "/accounts/0123456790/balances", access); status != http.StatusForbidden {
		t.Fatalf("unshared account: %d", status)
	}
	if status, body := e.get(e.good, "/discover", ""); status != 200 || body["tls_client_certificate_bound_access_tokens"] != true {
		t.Fatalf("discover: %d %v", status, body)
	}
	c, _ := e.srv.Consent(context.Background(), id)
	if c.Status != Authorised || c.Customer != "adaeze" || c.LastUsedAt.IsZero() {
		t.Fatalf("consent %+v", c)
	}
	hist, _ := e.srv.AccessHistory(context.Background(), "adaeze", 10)
	if len(hist) == 0 {
		t.Fatal("no access history")
	}
	for k, v := range map[string]string{"access": access, "refresh": tok["refresh_token"].(string)} {
		stored, err := e.store.Token(context.Background(), Hash(v))
		if err != nil || stored.Kind != k || stored.Hash == v {
			t.Fatalf("%s token is not stored as a hash", k)
		}
	}
}

func TestPAR(t *testing.T) {
	e := newEnv(t)
	base := func() url.Values {
		return url.Values{"response_type": {"code"}, "redirect_uri": {"https://connect.localhost/cb"}, "code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}, "scope": {allScopes}}
	}
	plain := base()
	plain.Set("code_challenge_method", "plain")
	wrongURI := base()
	wrongURI.Set("redirect_uri", "https://connect.localhost/cb/")
	for name, f := range map[string]url.Values{"plain PKCE": plain, "redirect not an exact match": wrongURI} {
		if status, body := e.post(e.good, "/par", f); status != http.StatusBadRequest {
			t.Errorf("%s: %d %v", name, status, body)
		}
	}
	if status, _ := e.post(e.good, "/par", url.Values{"client_assertion": {"junk"}, "client_assertion_type": {assertionType}}); status != http.StatusUnauthorized {
		t.Errorf("junk assertion: %d", status)
	}
	if status, _ := e.post(e.good, "/par", url.Values{"client_assertion": {"x"}}); status != http.StatusUnauthorized {
		t.Errorf("missing assertion type: %d", status)
	}
	f := base()
	f.Set("client_assertion", e.assertion())
	f.Set("client_assertion_type", assertionType)
	if status, _ := e.post(e.good, "/par", f); status != http.StatusCreated {
		t.Fatalf("first push: %d", status)
	}
	if status, _ := e.post(e.good, "/par", f); status != http.StatusUnauthorized {
		t.Errorf("replayed assertion: %d", status)
	}
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	forged, _ := Sign(other, "k", map[string]any{"iss": "OBR-1", "sub": "OBR-1", "aud": e.ts.URL, "jti": "z", "exp": e.now.Add(time.Minute).Unix()})
	if status, _ := e.post(e.good, "/par", url.Values{"client_assertion": {forged}, "client_assertion_type": {assertionType}}); status != http.StatusUnauthorized {
		t.Errorf("assertion signed by another key: %d", status)
	}
}

func TestRequestIsSingleUse(t *testing.T) {
	e := newEnv(t)
	uri, _ := e.pushed()
	if _, err := e.srv.Request(context.Background(), uri, "OBR-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.srv.Request(context.Background(), uri, "someone-else"); err == nil {
		t.Fatal("request_uri worked for another client")
	}
	if _, err := e.srv.Approve(context.Background(), uri, "adaeze", nil, false); err != ErrNoAccounts {
		t.Fatalf("approve without accounts: %v", err)
	}
	if _, err := e.srv.Approve(context.Background(), uri, "adaeze", []string{"0123456789"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.srv.Approve(context.Background(), uri, "adaeze", []string{"0123456789"}, false); err == nil {
		t.Fatal("request_uri was approved twice")
	}
}

func TestRejectRedirectsWithIss(t *testing.T) {
	e := newEnv(t)
	uri, id := e.pushed()
	loc, err := e.srv.Reject(context.Background(), uri)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(loc)
	if u.Query().Get("error") != "access_denied" || u.Query().Get("iss") != e.ts.URL {
		t.Fatalf("redirect %s", loc)
	}
	if c, _ := e.srv.Consent(context.Background(), id); c.Status != Rejected {
		t.Fatalf("status %s", c.Status)
	}
}

func TestPKCEFailureBurnsTheCode(t *testing.T) {
	e := newEnv(t)
	code, _ := e.code()
	if status, body := e.exchange(e.good, code, "wrong-verifier-wrong-verifier-wrong-verifier-1"); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("wrong verifier: %d %v", status, body)
	}
	if status, _ := e.exchange(e.good, code, verifier); status != http.StatusBadRequest {
		t.Fatalf("code worked after a failed attempt: %d", status)
	}
}

func TestCodeReuseRevokesTokens(t *testing.T) {
	e := newEnv(t)
	code, _ := e.code()
	status, body := e.exchange(e.good, code, verifier)
	if status != 200 {
		t.Fatal(status, body)
	}
	if status, _ := e.exchange(e.good, code, verifier); status != http.StatusBadRequest {
		t.Fatalf("second use: %d", status)
	}
	if status, body := e.get(e.good, "/accounts", body["access_token"].(string)); status != http.StatusUnauthorized {
		t.Fatalf("token from a reused code still works: %d %v", status, body)
	}
}

func TestExpiredCode(t *testing.T) {
	e := newEnv(t)
	code, _ := e.code()
	e.advance(61*time.Second, 0)
	if status, body := e.exchange(e.good, code, verifier); status != http.StatusBadRequest || !strings.Contains(body["error_description"].(string), "expired") {
		t.Fatalf("expired code: %d %v", status, body)
	}
}

func TestWrongCertificate(t *testing.T) {
	e := newEnv(t)
	code, _ := e.code()
	for name, c := range map[string]*http.Client{
		"unregistered name": e.clientFor("stranger", "aggregator"),
		"wrong role":        e.clientFor("connect", "partner"),
	} {
		if status, _ := e.exchange(c, code, verifier); status != http.StatusUnauthorized && status != http.StatusForbidden {
			t.Errorf("%s: %d", name, status)
		}
	}
	status, body := e.exchange(e.good, code, verifier)
	if status != 200 {
		t.Fatalf("the real client was locked out: %d %v", status, body)
	}
	access := body["access_token"].(string)
	if status, body := e.get(e.other, "/accounts", access); status != http.StatusUnauthorized || body["error"] != "invalid_token" {
		t.Fatalf("token used with another certificate: %d %v", status, body)
	}
	if status, _ := e.get(e.good, "/accounts", access); status != 200 {
		t.Fatalf("bound certificate refused: %d", status)
	}
	refresh := body["refresh_token"].(string)
	if status, _ := e.post(e.other, "/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}}); status != http.StatusBadRequest {
		t.Fatalf("refresh from another certificate: %d", status)
	}
}

func TestRefreshGrace(t *testing.T) {
	e := newEnv(t)
	tok, _ := e.tokens()
	first := tok["refresh_token"].(string)
	refresh := func(token string) (int, map[string]any) {
		return e.post(e.good, "/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {token}})
	}
	status, second := refresh(first)
	if status != 200 || second["refresh_token"] == first {
		t.Fatalf("refresh: %d %v", status, second)
	}
	e.advance(30*time.Second, 0)
	status, third := refresh(first)
	if status != 200 {
		t.Fatalf("previous refresh token inside the grace period: %d %v", status, third)
	}
	if status, _ := e.get(e.good, "/accounts", second["access_token"].(string)); status != http.StatusUnauthorized {
		t.Fatalf("the lost-response access token should be replaced: %d", status)
	}
	e.advance(2*time.Minute, 0)
	if status, body := refresh(first); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("previous refresh token after the grace period: %d %v", status, body)
	}
	if status, _ := refresh(third["refresh_token"].(string)); status != http.StatusBadRequest {
		t.Fatalf("reuse should revoke the whole family: %d", status)
	}
}

func TestRevokedConsent(t *testing.T) {
	e := newEnv(t)
	tok, id := e.tokens()
	access := tok["access_token"].(string)
	if err := e.srv.Revoke(context.Background(), id, "someone-else", "bank"); err != ErrNotYours {
		t.Fatalf("revoke by another customer: %v", err)
	}
	if err := e.srv.Revoke(context.Background(), id, "adaeze", "bank"); err != nil {
		t.Fatal(err)
	}
	if status, body := e.get(e.good, "/accounts", access); status != http.StatusForbidden || body["error"] != "consent_revoked" {
		t.Fatalf("after revoke: %d %v", status, body)
	}
	if status, body := e.post(e.good, "/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok["refresh_token"].(string)}}); status != http.StatusBadRequest {
		t.Fatalf("refresh after revoke: %d %v", status, body)
	}
	last := e.ledger.events[len(e.ledger.events)-1]
	if last.Status != Revoked || last.ConsentID != id || last.Via != "bank" {
		t.Fatalf("ledger %+v", last)
	}
}

const day = 24 * time.Hour

func TestConsentNeedsRenewalAfterAYear(t *testing.T) {
	e := newEnv(t)
	tok, id := e.tokens()
	e.advance(0, 366*day)
	if status, body := e.get(e.good, "/accounts", tok["access_token"].(string)); status != http.StatusForbidden || body["error"] != "consent_reconsent_required" {
		t.Fatalf("after a year: %d %v", status, body)
	}
	if c, _ := e.store.Consent(context.Background(), id); c.Status != ReconsentRequired {
		t.Fatalf("status %s", c.Status)
	}
	if last := e.ledger.events[len(e.ledger.events)-1]; last.Status != ReconsentRequired {
		t.Fatalf("ledger %+v", last)
	}
}

func TestConsentLifetimeFollowsTheSetting(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.ConsentMonths = 6 })
	_, id := e.tokens()
	c, _ := e.store.Consent(context.Background(), id)
	if want := addMonths(c.AuthorisedAt, 6); !c.ExpiresAt.Equal(want) {
		t.Fatalf("expires %s, want %s", c.ExpiresAt, want)
	}
	uri, _ := e.pushed()
	req, err := e.srv.Request(context.Background(), uri, "OBR-1")
	if err != nil || req.Months != 6 || !req.Until.Equal(addMonths(e.biz, 6)) {
		t.Fatalf("request %+v %v", req, err)
	}
}

func TestDormantConsent(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.ConsentMonths = 24 })
	tok, id := e.tokens()
	access := tok["access_token"].(string)
	e.advance(0, 179*day)
	if status, body := e.get(e.good, "/accounts", access); status != 200 {
		t.Fatalf("used within 180 days: %d %v", status, body)
	}
	e.advance(0, 179*day)
	if status, body := e.get(e.good, "/accounts", access); status != 200 {
		t.Fatalf("use should restart the 180 days: %d %v", status, body)
	}
	e.advance(0, 181*day)
	if status, body := e.get(e.good, "/accounts", access); status != http.StatusForbidden || body["error"] != "consent_dormant" {
		t.Fatalf("unused for 181 days: %d %v", status, body)
	}
	if c, _ := e.store.Consent(context.Background(), id); c.Status != Dormant {
		t.Fatalf("status %s", c.Status)
	}
	if status, _ := e.post(e.good, "/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok["refresh_token"].(string)}}); status != http.StatusBadRequest {
		t.Fatalf("refresh of a dormant consent: %d", status)
	}
	if last := e.ledger.events[len(e.ledger.events)-1]; last.Status != Dormant || last.ConsentID != id {
		t.Fatalf("ledger %+v", last)
	}
}

func TestDormancyFollowsTheSetting(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.DormantDays = 30 })
	tok, _ := e.tokens()
	e.advance(0, 31*day)
	if status, body := e.get(e.good, "/accounts", tok["access_token"].(string)); status != http.StatusForbidden || body["error"] != "consent_dormant" {
		t.Fatalf("unused for 31 days with a 30 day setting: %d %v", status, body)
	}
}

func TestScopeNarrowsWhatIsShared(t *testing.T) {
	e := newEnv(t)
	for name, scope := range map[string]string{"none": "", "unknown": "accounts payments"} {
		status, body := e.post(e.good, "/par", url.Values{"response_type": {"code"}, "redirect_uri": {"https://connect.localhost/cb"}, "code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}, "scope": {scope}})
		if status != http.StatusBadRequest || body["error"] != "invalid_scope" {
			t.Errorf("%s scope: %d %v", name, status, body)
		}
	}
	tok, id := e.tokensFor("balances accounts")
	if tok["scope"] != "accounts balances" {
		t.Fatalf("scope %v", tok["scope"])
	}
	access := tok["access_token"].(string)
	if status, body := e.get(e.good, "/accounts/0123456789/balances", access); status != 200 {
		t.Fatalf("balances: %d %v", status, body)
	}
	if status, body := e.get(e.good, "/accounts/0123456789/transactions", access); status != http.StatusForbidden || body["error"] != "insufficient_scope" {
		t.Fatalf("transactions without the scope: %d %v", status, body)
	}
	if c, _ := e.store.Consent(context.Background(), id); strings.Join(c.Scopes, " ") != "accounts balances" {
		t.Fatalf("stored scopes %v", c.Scopes)
	}
}

func TestTransactionCursor(t *testing.T) {
	e := newEnv(t)
	tok, _ := e.tokens()
	access := tok["access_token"].(string)
	seen, cursor := []string{}, ""
	for i := 0; i < 5; i++ {
		path := "/accounts/0123456789/transactions?page_size=2"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		status, body := e.get(e.good, path, access)
		if status != 200 {
			t.Fatalf("page %d: %d %v", i, status, body)
		}
		for _, row := range body["data"].([]any) {
			seen = append(seen, row.(map[string]any)["id"].(string))
		}
		next, _ := body["next_cursor"].(string)
		if next == "" {
			break
		}
		cursor = next
	}
	if strings.Join(seen, "") != "abcde" {
		t.Fatalf("paged through %v", seen)
	}
	for _, bad := range []string{"9223372036854775807", "2026-01-05.99999999999999999999", "x.1", "2026-01-05"} {
		if status, _ := e.get(e.good, "/accounts/0123456789/transactions?cursor="+url.QueryEscape(bad), access); status != http.StatusBadRequest {
			t.Errorf("cursor %q: %d", bad, status)
		}
	}
	if status, body := e.get(e.good, "/accounts/0123456789/transactions?cursor=2099-01-01.0", access); status != 200 || len(body["data"].([]any)) != 0 || body["next_cursor"] != nil {
		t.Errorf("cursor past the end: %d %v", status, body)
	}
}

func TestTransactionsReportTheWindow(t *testing.T) {
	e := newEnv(t)
	tok, _ := e.tokens()
	access := tok["access_token"].(string)
	want := clock.DateOf(e.biz).AddDays(defaultLookaheadDays).String()
	if status, body := e.get(e.good, "/accounts/0123456789/transactions", access); status != 200 || body["to"] != want {
		t.Errorf("default window: %d %v, want to %s", status, body, want)
	}
	if status, body := e.get(e.good, "/accounts/0123456789/transactions?to=2026-01-06", access); status != 200 || body["to"] != "2026-01-06" {
		t.Errorf("requested window: %d %v", status, body)
	}
}

func TestRevokeNeedsTheCustomer(t *testing.T) {
	e := newEnv(t)
	_, id := e.tokens()
	if err := e.srv.Revoke(context.Background(), id, "", "bank"); err != ErrNotYours {
		t.Fatalf("revoke without a customer: %v", err)
	}
	if c, _ := e.store.Consent(context.Background(), id); c.Status != Authorised {
		t.Fatalf("status %s", c.Status)
	}
}

func TestStaleRequestsAreDropped(t *testing.T) {
	e := newEnv(t)
	_, stale := e.pushed()
	code, _ := e.code()
	e.advance(11*time.Minute, 0)
	e.pushed()
	e.srv.mu.Lock()
	pars, codes := len(e.srv.pars), len(e.srv.codes)
	e.srv.mu.Unlock()
	if pars != 1 || codes != 1 {
		t.Fatalf("%d requests and %d codes kept", pars, codes)
	}
	if c, _ := e.store.Consent(context.Background(), stale); c.Status != Expired {
		t.Fatalf("an abandoned request is %s", c.Status)
	}
	e.advance(11*time.Minute, 0)
	if status, _ := e.post(e.good, "/token", url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {"https://connect.localhost/cb"}}); status != http.StatusBadRequest {
		t.Fatalf("expired code: %d", status)
	}
	e.srv.mu.Lock()
	codes = len(e.srv.codes)
	e.srv.mu.Unlock()
	if codes != 0 {
		t.Fatalf("%d codes kept", codes)
	}
}

func TestResumeExpiresLostRequests(t *testing.T) {
	e := newEnv(t)
	_, id := e.pushed()
	restarted := New(e.srv.cfg)
	if err := restarted.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c, _ := e.store.Consent(context.Background(), id); c.Status != Expired {
		t.Fatalf("status %s", c.Status)
	}
}

type failingStore struct {
	*MemStore
}

func (failingStore) RecordAccess(context.Context, Access) error {
	return errors.New("disk full")
}

func TestAccessLogFailureFailsTheRequest(t *testing.T) {
	e := newEnv(t)
	tok, _ := e.tokens()
	e.srv.cfg.Store = failingStore{e.store}
	if status, body := e.get(e.good, "/accounts/0123456789/balances", tok["access_token"].(string)); status != http.StatusInternalServerError || body["data"] != nil {
		t.Fatalf("balances with a broken access log: %d %v", status, body)
	}
}

func TestAssertionRules(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.AssertionLife = time.Minute })
	sign := func(aud string, life time.Duration) string {
		jwt, err := Sign(e.key, "k1", map[string]any{"iss": "OBR-1", "sub": "OBR-1", "aud": aud, "jti": random("j", 8), "exp": e.now.Add(life).Unix()})
		if err != nil {
			t.Fatal(err)
		}
		return jwt
	}
	push := func(assertion string) int {
		status, _ := e.post(e.good, "/par", url.Values{"response_type": {"code"}, "redirect_uri": {"https://connect.localhost/cb"}, "code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}, "scope": {allScopes}, "client_assertion": {assertion}, "client_assertion_type": {assertionType}})
		return status
	}
	if status := push(sign(e.ts.URL, 30*time.Second)); status != http.StatusCreated {
		t.Errorf("issuer audience: %d", status)
	}
	if status := push(sign(e.ts.URL+"/par", 30*time.Second)); status != http.StatusUnauthorized {
		t.Errorf("endpoint as audience: %d", status)
	}
	if status := push(sign(e.ts.URL, 2*time.Minute)); status != http.StatusUnauthorized {
		t.Errorf("assertion longer than the configured window: %d", status)
	}
}

func TestAddMonthsClampsToMonthEnd(t *testing.T) {
	at := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 9, 30, 0, 0, time.UTC) }
	for _, c := range []struct {
		from   time.Time
		months int
		want   time.Time
	}{
		{at(2028, time.February, 29), 12, at(2029, time.February, 28)},
		{at(2028, time.February, 29), 48, at(2032, time.February, 29)},
		{at(2026, time.August, 31), 6, at(2027, time.February, 28)},
		{at(2027, time.August, 31), 6, at(2028, time.February, 29)},
		{at(2026, time.January, 31), 1, at(2026, time.February, 28)},
		{at(2026, time.November, 30), 3, at(2027, time.February, 28)},
		{at(2026, time.March, 15), 12, at(2027, time.March, 15)},
		{at(2026, time.December, 31), 2, at(2027, time.February, 28)},
	} {
		if got := addMonths(c.from, c.months); !got.Equal(c.want) {
			t.Errorf("%s + %d months = %s, want %s", c.from.Format("2006-01-02"), c.months, got.Format("2006-01-02"), c.want.Format("2006-01-02"))
		}
	}
}

func TestConsentAuthorisedOnAMonthEndExpiresOnTheLastDay(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.ConsentMonths = 6; c.DormantDays = 400 })
	start := time.Date(2026, time.August, 31, 9, 0, 0, 0, time.UTC)
	e.now, e.biz = start, start
	tok, id := e.tokens()
	c, _ := e.store.Consent(context.Background(), id)
	if want := time.Date(2027, time.February, 28, 9, 0, 0, 0, time.UTC); !c.ExpiresAt.Equal(want) {
		t.Fatalf("expires %s, want %s", c.ExpiresAt, want)
	}
	e.advance(0, 180*day)
	if status, body := e.get(e.good, "/accounts", tok["access_token"].(string)); status != http.StatusOK {
		t.Fatalf("before the last day: %d %v", status, body)
	}
	e.advance(0, 2*day)
	if status, body := e.get(e.good, "/accounts", tok["access_token"].(string)); status != http.StatusForbidden || body["error"] != "consent_reconsent_required" {
		t.Fatalf("after the last day: %d %v", status, body)
	}
}
