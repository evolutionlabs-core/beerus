package openbanking

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

type fakeRegistry struct {
	client *Client
}

func (r fakeRegistry) Client(id string) (*Client, bool) {
	return r.client, id == r.client.ID
}

func (r fakeRegistry) ClientByCaller(id string) (*Client, bool) {
	return r.client, id == r.client.CertCN
}

type fakeData struct{}

func (fakeData) Accounts(_ context.Context, customer string) ([]AccountInfo, error) {
	return []AccountInfo{{ID: "0123456789", Name: "Current", Type: "current", Currency: "NGN"}, {ID: "0123456790", Name: "Savings", Type: "savings", Currency: "NGN"}}, nil
}

func (fakeData) Account(_ context.Context, id string) (AccountInfo, error) {
	return AccountInfo{ID: id, Name: "Current", Type: "current", Currency: "NGN"}, nil
}

func (fakeData) Balance(context.Context, string) (Balance, error) {
	return Balance{Ledger: 5000, Available: 4000, Currency: "NGN"}, nil
}

func (fakeData) BalanceOn(context.Context, string, clock.Date) (int64, error) {
	return 4200, nil
}

func (fakeData) Transactions(context.Context, string, clock.Date, clock.Date) ([]Txn, error) {
	d, _ := clock.ParseDate("2026-01-05")
	out := []Txn{}
	for i := 0; i < 5; i++ {
		out = append(out, Txn{ID: string(rune('a' + i)), Amount: int64(i), ValueDate: d, Seq: int64(i), BookedAt: time.Now()})
	}
	return out, nil
}

func (fakeData) Ping(context.Context) error {
	return nil
}

type env struct {
	t     *testing.T
	srv   *Server
	ts    *httptest.Server
	store *MemStore
	key   *ecdsa.PrivateKey
	good  *http.Client
}

func (e *env) lastLedger() ConsentEvent {
	e.t.Helper()
	due, err := e.srv.LedgerDue(context.Background(), 100)
	if err != nil || len(due) == 0 {
		e.t.Fatalf("ledger events %+v: %v", due, err)
	}
	return due[len(due)-1].ConsentEvent
}

type callerTransport struct {
	id string
}

func (c callerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set(config.CallerHeader, c.id)
	return http.DefaultTransport.RoundTrip(r)
}

func clientFor(id string) *http.Client {
	return &http.Client{Transport: callerTransport{id}}
}

func newEnv(t *testing.T, tweaks ...func(*Config)) *env {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, store: NewMemStore(), key: key}
	var h http.Handler
	e.ts = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) }))
	e.ts.Start()
	t.Cleanup(e.ts.Close)
	client := &Client{ID: "OBR-1", Name: "Connect", Role: "aggregator", CertCN: "connect", Keys: []*ecdsa.PublicKey{&key.PublicKey}, RedirectURIs: []string{"http://connect.localhost/cb"}}
	cfg := Config{
		BankID: "bank-a", Terms: Terms{BankName: "Bank A"}, Issuer: e.ts.URL, AuthorizeURL: "http://bank-a.localhost/authorize", HistoryDays: 90,
		Store: e.store, Registry: fakeRegistry{client}, Data: fakeData{},
	}
	for _, tweak := range tweaks {
		tweak(&cfg)
	}
	e.srv = New(cfg)
	h = e.srv.Handler()
	e.good = clientFor("connect")
	return e
}

func (e *env) editConsent(id string, edit func(*Consent)) {
	e.t.Helper()
	c, err := e.store.Consent(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	edit(c)
	if err := e.store.UpdateConsent(context.Background(), c); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) editToken(raw string, edit func(*Token)) {
	e.t.Helper()
	e.store.mu.Lock()
	defer e.store.mu.Unlock()
	t, ok := e.store.tokens[Hash(raw)]
	if !ok {
		e.t.Fatal("token not stored")
	}
	edit(t)
}

func (e *env) ageRequests(par, code time.Duration) {
	e.srv.mu.Lock()
	defer e.srv.mu.Unlock()
	now := time.Now()
	for _, p := range e.srv.pars {
		p.expires = now.Add(-par)
	}
	for _, c := range e.srv.codes {
		c.expires = now.Add(-code)
	}
}

func (e *env) assertion() string {
	e.t.Helper()
	jwt, err := Sign(e.key, "k1", map[string]any{"iss": "OBR-1", "sub": "OBR-1", "aud": e.ts.URL, "jti": random("j", 8), "exp": time.Now().Add(time.Minute).Unix(), "iat": time.Now().Unix()})
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
		"response_type": {"code"}, "client_id": {"OBR-1"}, "redirect_uri": {"http://connect.localhost/cb"}, "state": {"st1"},
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
	return e.post(c, "/token", url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {v}, "redirect_uri": {"http://connect.localhost/cb"}})
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
	if status, body := e.get(e.good, "/discover", ""); status != 200 || body["token_endpoint"] != e.ts.URL+"/token" {
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
		return url.Values{"response_type": {"code"}, "redirect_uri": {"http://connect.localhost/cb"}, "code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}, "scope": {allScopes}}
	}
	plain := base()
	plain.Set("code_challenge_method", "plain")
	wrongURI := base()
	wrongURI.Set("redirect_uri", "http://connect.localhost/cb/")
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
	forged, _ := Sign(other, "k", map[string]any{"iss": "OBR-1", "sub": "OBR-1", "aud": e.ts.URL, "jti": "z", "exp": time.Now().Add(time.Minute).Unix()})
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
	e.ageRequests(time.Second, time.Second)
	if status, body := e.exchange(e.good, code, verifier); status != http.StatusBadRequest || !strings.Contains(body["error_description"].(string), "expired") {
		t.Fatalf("expired code: %d %v", status, body)
	}
}

func TestWrongCaller(t *testing.T) {
	e := newEnv(t)
	code, _ := e.code()
	rows := []struct {
		name   string
		client *http.Client
		status int
		desc   string
	}{
		{"no caller", &http.Client{}, http.StatusUnauthorized, "Send your ID in the Beerus-Caller header."},
		{"unregistered ID", clientFor("stranger"), http.StatusUnauthorized, "stranger is not registered at the Registry."},
	}
	for _, r := range rows {
		status, body := e.exchange(r.client, code, verifier)
		if status != r.status || body["error"] != "invalid_client" || body["error_description"] != r.desc {
			t.Errorf("%s: %d %v, want %d invalid_client %q", r.name, status, body, r.status, r.desc)
		}
	}
	status, body := e.exchange(e.good, code, verifier)
	if status != 200 {
		t.Fatalf("the real client was locked out: %d %v", status, body)
	}
	access := body["access_token"].(string)
	if status, body := e.get(&http.Client{}, "/accounts", access); status != http.StatusUnauthorized || body["error"] != "invalid_client" {
		t.Fatalf("token used with no caller: %d %v", status, body)
	}
	if status, _ := e.get(e.good, "/accounts", access); status != 200 {
		t.Fatalf("registered client refused: %d", status)
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
	status, third := refresh(first)
	if status != 200 {
		t.Fatalf("previous refresh token inside the grace period: %d %v", status, third)
	}
	if status, _ := e.get(e.good, "/accounts", second["access_token"].(string)); status != http.StatusUnauthorized {
		t.Fatalf("the lost-response access token should be replaced: %d", status)
	}
	e.editToken(first, func(t *Token) { t.RotatedAt = time.Now().Add(-50 * time.Second) })
	if status, body := refresh(first); status != 200 {
		t.Fatalf("previous refresh token inside the grace period, rotated 50 seconds ago: %d %v", status, body)
	}
	e.editToken(first, func(t *Token) { t.RotatedAt = time.Now().Add(-70 * time.Second) })
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
	last := e.lastLedger()
	if last.Status != Revoked || last.ConsentID != id || last.Via != "bank" {
		t.Fatalf("ledger %+v", last)
	}
}

const day = 24 * time.Hour

func TestConsentNeedsRenewalAfterAYear(t *testing.T) {
	e := newEnv(t)
	tok, id := e.tokens()
	e.editConsent(id, func(c *Consent) { c.ExpiresAt = time.Now().Add(-time.Second) })
	if status, body := e.get(e.good, "/accounts", tok["access_token"].(string)); status != http.StatusForbidden || body["error"] != "consent_reconsent_required" {
		t.Fatalf("after a year: %d %v", status, body)
	}
	if c, _ := e.store.Consent(context.Background(), id); c.Status != ReconsentRequired {
		t.Fatalf("status %s", c.Status)
	}
	if last := e.lastLedger(); last.Status != ReconsentRequired {
		t.Fatalf("ledger %+v", last)
	}
}

func TestConsentLifetimeFollowsTheSetting(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Terms.ConsentMonths = 6 })
	_, id := e.tokens()
	c, _ := e.store.Consent(context.Background(), id)
	if want := addMonths(c.AuthorisedAt, 6); !c.ExpiresAt.Equal(want) {
		t.Fatalf("expires %s, want %s", c.ExpiresAt, want)
	}
	uri, _ := e.pushed()
	before := e.srv.cfg.Clock.Now()
	req, err := e.srv.Request(context.Background(), uri, "OBR-1")
	after := e.srv.cfg.Clock.Now()
	if err != nil || req.Months != 6 || req.Until.Before(addMonths(before, 6)) || req.Until.After(addMonths(after, 6)) {
		t.Fatalf("request %+v %v", req, err)
	}
}

func TestSetTermsKeepsPendingRequests(t *testing.T) {
	e := newEnv(t)
	uri, _ := e.pushed()
	e.srv.SetTerms(Terms{BankName: "Bank B", ConsentMonths: 6})
	if _, body := e.get(e.good, "/discover", ""); body["name"] != "Bank B" {
		t.Fatalf("discover name %v", body["name"])
	}
	req, err := e.srv.Request(context.Background(), uri, "OBR-1")
	if err != nil || req.Months != 6 {
		t.Fatalf("request %+v %v", req, err)
	}
	e.srv.SetTerms(Terms{})
	if req, err := e.srv.Request(context.Background(), uri, "OBR-1"); err != nil || req.Months != DefaultConsentMonths {
		t.Fatalf("request %+v %v", req, err)
	}
}

func TestDormantConsent(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Terms.ConsentMonths = 24 })
	tok, id := e.tokens()
	access := tok["access_token"].(string)
	before := e.srv.cfg.Clock.Now()
	if status, body := e.get(e.good, "/accounts", access); status != 200 {
		t.Fatalf("used today: %d %v", status, body)
	}
	after := e.srv.cfg.Clock.Now()
	used, err := e.store.Consent(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if used.LastUsedAt.Before(before) || used.LastUsedAt.After(after) {
		t.Fatalf("last used %s, want between %s and %s", used.LastUsedAt, before, after)
	}
	e.editConsent(id, func(c *Consent) {
		c.AuthorisedAt = time.Now().Add(-400 * day)
		c.LastUsedAt = time.Now().Add(-181 * day)
	})
	if status, body := e.get(e.good, "/accounts", access); status != http.StatusForbidden || body["error"] != "consent_dormant" {
		t.Fatalf("unused for 181 days: %d %v", status, body)
	}
	if c, _ := e.store.Consent(context.Background(), id); c.Status != Dormant {
		t.Fatalf("status %s", c.Status)
	}
	if status, _ := e.post(e.good, "/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok["refresh_token"].(string)}}); status != http.StatusBadRequest {
		t.Fatalf("refresh of a dormant consent: %d", status)
	}
	if last := e.lastLedger(); last.Status != Dormant || last.ConsentID != id {
		t.Fatalf("ledger %+v", last)
	}
}

func TestDormancyFollowsTheSetting(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Terms.DormantDays = 30 })
	tok, id := e.tokens()
	e.editConsent(id, func(c *Consent) { c.AuthorisedAt = time.Now().Add(-31 * day) })
	if status, body := e.get(e.good, "/accounts", tok["access_token"].(string)); status != http.StatusForbidden || body["error"] != "consent_dormant" {
		t.Fatalf("unused for 31 days with a 30 day setting: %d %v", status, body)
	}
}

func TestScopeNarrowsWhatIsShared(t *testing.T) {
	e := newEnv(t)
	for name, scope := range map[string]string{"none": "", "unknown": "accounts payments"} {
		status, body := e.post(e.good, "/par", url.Values{"response_type": {"code"}, "redirect_uri": {"http://connect.localhost/cb"}, "code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}, "scope": {scope}})
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
	want := clock.DateOf(time.Now()).AddDays(defaultLookaheadDays).String()
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
	e.ageRequests(11*time.Minute, 5*time.Minute)
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
	e.ageRequests(0, 11*time.Minute)
	if status, _ := e.post(e.good, "/token", url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {"http://connect.localhost/cb"}}); status != http.StatusBadRequest {
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
	e := newEnv(t, func(c *Config) { c.Terms.AssertionLife = time.Minute })
	sign := func(aud string, life time.Duration) string {
		jwt, err := Sign(e.key, "k1", map[string]any{"iss": "OBR-1", "sub": "OBR-1", "aud": aud, "jti": random("j", 8), "exp": time.Now().Add(life).Unix()})
		if err != nil {
			t.Fatal(err)
		}
		return jwt
	}
	push := func(assertion string) int {
		status, _ := e.post(e.good, "/par", url.Values{"response_type": {"code"}, "redirect_uri": {"http://connect.localhost/cb"}, "code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}, "scope": {allScopes}, "client_assertion": {assertion}, "client_assertion_type": {assertionType}})
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

func TestLedgerOutboxKeepsOrderAndRetries(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, id := e.tokens()
	if err := e.srv.Revoke(ctx, id, "adaeze", "bank"); err != nil {
		t.Fatal(err)
	}
	due, err := e.srv.LedgerDue(ctx, 10)
	if err != nil || len(due) != 2 || due[0].Status != Authorised || due[1].Status != Revoked || due[0].ID >= due[1].ID {
		t.Fatalf("pending ledger events %+v: %v", due, err)
	}
	if err := e.srv.Retry(ctx, due[0], time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if again, _ := e.srv.LedgerDue(ctx, 10); len(again) != 0 {
		t.Fatalf("a later event is due while the first waits: %+v", again)
	}
	if conn, _ := e.srv.Due(ctx, 10); len(conn) != 2 {
		t.Fatalf("%d events due for Connect", len(conn))
	}
	again, _ := e.store.LedgerDue(ctx, time.Now().Add(2*time.Minute), 10)
	if len(again) != 2 || again[0].ID != due[0].ID || again[0].Attempts != 1 {
		t.Fatalf("after the wait: %+v", again)
	}
	for _, d := range again {
		if err := e.srv.Delivered(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	if left, _ := e.srv.LedgerDue(ctx, 10); len(left) != 0 {
		t.Fatalf("delivered events still due: %+v", left)
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

func TestExpiredTokensAreRefused(t *testing.T) {
	e := newEnv(t)
	tok, _ := e.tokens()
	access, refresh := tok["access_token"].(string), tok["refresh_token"].(string)
	past := time.Now().Add(-time.Second)
	e.editToken(access, func(x *Token) { x.ExpiresAt = past })
	if status, body := e.get(e.good, "/accounts", access); status != http.StatusUnauthorized || body["error"] != "invalid_token" {
		t.Fatalf("expired access token: %d %v", status, body)
	}
	e.editToken(refresh, func(x *Token) { x.ExpiresAt = past })
	if status, body := e.post(e.good, "/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}}); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("expired refresh token: %d %v", status, body)
	}
}

func TestIssuedTokenLifetimes(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Terms.AccessLife = 3 * time.Minute; c.Terms.RefreshDays = 10 })
	before := time.Now()
	tok, _ := e.tokens()
	after := time.Now()
	for kind, raw := range map[string]string{"access": tok["access_token"].(string), "refresh": tok["refresh_token"].(string)} {
		stored, err := e.store.Token(context.Background(), Hash(raw))
		if err != nil {
			t.Fatal(err)
		}
		min, max := before.Add(3*time.Minute), after.Add(3*time.Minute)
		if kind == "refresh" {
			min, max = before.AddDate(0, 0, 10), after.AddDate(0, 0, 10)
		}
		if stored.ExpiresAt.Before(min) || stored.ExpiresAt.After(max) {
			t.Errorf("%s token expires %s, want between %s and %s", kind, stored.ExpiresAt, min, max)
		}
	}
	if tok["expires_in"] != float64(180) {
		t.Errorf("expires_in %v", tok["expires_in"])
	}
}

func TestSetTermsChangesNextAccessLifetime(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Terms.AccessLife = 3 * time.Minute })
	e.srv.SetTerms(Terms{BankName: "Bank A", AccessLife: 20 * time.Minute})
	before := time.Now()
	tok, _ := e.tokens()
	after := time.Now()
	stored, err := e.store.Token(context.Background(), Hash(tok["access_token"].(string)))
	if err != nil {
		t.Fatal(err)
	}
	if stored.ExpiresAt.Before(before.Add(20*time.Minute)) || stored.ExpiresAt.After(after.Add(20*time.Minute)) {
		t.Errorf("access token expires %s, want 20 minutes from issue", stored.ExpiresAt)
	}
	if tok["expires_in"] != float64(1200) {
		t.Errorf("expires_in %v, want 1200", tok["expires_in"])
	}
	e.srv.SetTerms(Terms{BankName: "Bank A"})
	if tok, _ := e.tokens(); tok["expires_in"] != float64(600) {
		t.Errorf("default expires_in %v, want 600", tok["expires_in"])
	}
}

func TestLifetimeSettingsAreClamped(t *testing.T) {
	for _, c := range []struct {
		name string
		in   Config
		want Config
	}{
		{"defaults", Config{}, Config{CodeLife: time.Minute, RequestLife: 10 * time.Minute, RefreshGrace: time.Minute}},
		{"too long", Config{CodeLife: time.Hour, RequestLife: time.Hour}, Config{CodeLife: time.Minute, RequestLife: 10 * time.Minute, RefreshGrace: time.Minute}},
		{"shorter is kept", Config{CodeLife: 20 * time.Second, RequestLife: 2 * time.Minute, RefreshGrace: 5 * time.Second}, Config{CodeLife: 20 * time.Second, RequestLife: 2 * time.Minute, RefreshGrace: 5 * time.Second}},
	} {
		got := New(c.in).cfg
		if got.CodeLife != c.want.CodeLife || got.RequestLife != c.want.RequestLife || got.RefreshGrace != c.want.RefreshGrace {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestLapsed(t *testing.T) {
	at := time.Date(2026, time.March, 1, 9, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name string
		now  time.Time
		want bool
	}{
		{"a second before", at.Add(-time.Second), false},
		{"at the instant", at, false},
		{"a nanosecond after", at.Add(time.Nanosecond), true},
		{"a day after", at.Add(day), true},
	} {
		if got := lapsed(c.now, at); got != c.want {
			t.Errorf("%s: lapsed %v, want %v", c.name, got, c.want)
		}
	}
	issued := time.Date(2026, time.March, 1, 9, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name string
		life time.Duration
		want time.Time
	}{
		{"auth code", time.Minute, time.Date(2026, time.March, 1, 9, 1, 0, 0, time.UTC)},
		{"pushed request", 10 * time.Minute, time.Date(2026, time.March, 1, 9, 10, 0, 0, time.UTC)},
		{"access token", 10 * time.Minute, time.Date(2026, time.March, 1, 9, 10, 0, 0, time.UTC)},
	} {
		if got := expiresAt(issued, c.life); !got.Equal(c.want) {
			t.Errorf("%s expires %s, want %s", c.name, got, c.want)
		}
	}
}

func TestAssertionWindow(t *testing.T) {
	now := time.Date(2026, time.March, 1, 9, 0, 0, 0, time.UTC)
	life := time.Minute
	for _, c := range []struct {
		name string
		exp  time.Time
		want bool
	}{
		{"already expired", now.Add(-time.Second), false},
		{"expires now", now, true},
		{"inside the window", now.Add(30 * time.Second), true},
		{"at the window edge", now.Add(life), true},
		{"past the window", now.Add(life + time.Second), false},
	} {
		if got := assertionWindow(c.exp, now, life); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestConsentStatus(t *testing.T) {
	authorised := time.Date(2026, time.March, 1, 9, 0, 0, 0, time.UTC)
	at := func(days int) time.Time { return authorised.AddDate(0, 0, days) }
	consent := func(status Status, expires time.Time, lastUsed time.Time) *Consent {
		return &Consent{Status: status, AuthorisedAt: authorised, ExpiresAt: expires, LastUsedAt: lastUsed}
	}
	year := addMonths(authorised, 12)
	for _, c := range []struct {
		name    string
		consent *Consent
		now     time.Time
		dormant int
		want    Status
	}{
		{"fresh", consent(Authorised, year, time.Time{}), authorised, 180, Authorised},
		{"day 179 unused", consent(Authorised, year, time.Time{}), at(179), 180, Authorised},
		{"day 180 unused", consent(Authorised, year, time.Time{}), at(180), 180, Dormant},
		{"day 181 unused", consent(Authorised, year, time.Time{}), at(181), 180, Dormant},
		{"use restarts the 180 days", consent(Authorised, year, at(179)), at(358), 180, Authorised},
		{"unused 180 days after the last use", consent(Authorised, year, at(179)), at(359), 180, Dormant},
		{"dormancy follows the setting", consent(Authorised, year, time.Time{}), at(31), 30, Dormant},
		{"just inside a 30 day setting", consent(Authorised, year, time.Time{}), at(29), 30, Authorised},
		{"last use before authorisation is ignored", consent(Authorised, year, at(-10)), at(179), 180, Authorised},
		{"day before the year ends", consent(Authorised, year, at(364)), at(364), 400, Authorised},
		{"year ends first day", consent(Authorised, year, at(365)), at(365), 400, ReconsentRequired},
		{"after a year", consent(Authorised, year, at(366)), at(366), 400, ReconsentRequired},
		{"expiry beats dormancy", consent(Authorised, year, time.Time{}), at(366), 180, ReconsentRequired},
		{"far future", consent(Authorised, year, time.Time{}), time.Date(2126, time.March, 1, 9, 0, 0, 0, time.UTC), 180, ReconsentRequired},
		{"revoked stays revoked", consent(Revoked, year, time.Time{}), at(1), 180, Revoked},
		{"awaiting stays awaiting", consent(AwaitingAuthorisation, time.Time{}, time.Time{}), at(400), 180, AwaitingAuthorisation},
	} {
		if got := consentStatus(c.consent, c.now, c.dormant); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestConsentOnAMonthEndExpiresOnTheLastDay(t *testing.T) {
	start := time.Date(2026, time.August, 31, 9, 0, 0, 0, time.UTC)
	expires := addMonths(start, 6)
	if want := time.Date(2027, time.February, 28, 9, 0, 0, 0, time.UTC); !expires.Equal(want) {
		t.Fatalf("expires %s, want %s", expires, want)
	}
	c := &Consent{Status: Authorised, AuthorisedAt: start, ExpiresAt: expires}
	for _, tc := range []struct {
		name string
		now  time.Time
		want Status
	}{
		{"180 days in", start.AddDate(0, 0, 180), Authorised},
		{"a second before the last day ends", expires.Add(-time.Second), Authorised},
		{"at the expiry", expires, ReconsentRequired},
		{"182 days in", start.AddDate(0, 0, 182), ReconsentRequired},
	} {
		if got := consentStatus(c, tc.now, 400); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestHistoryFloorAndAsOf(t *testing.T) {
	today := clock.Date{Y: 2026, M: time.March, D: 1}
	floor := clock.Date{Y: 2025, M: time.December, D: 1}
	for _, c := range []struct {
		name string
		d    string
		want bool
	}{
		{"yesterday", "2026-02-28", true},
		{"today", "2026-03-01", false},
		{"tomorrow", "2026-03-02", false},
		{"the day before the floor", "2025-11-30", true},
		{"two days before the floor", "2025-11-29", false},
		{"the floor", "2025-12-01", true},
	} {
		d, err := clock.ParseDate(c.d)
		if err != nil {
			t.Fatal(err)
		}
		if got := asOfAllowed(d, today, floor); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestBalanceAsOfFollowsTheHistoryWindow(t *testing.T) {
	for _, days := range []int{1, 90, 366} {
		e := newEnv(t, func(c *Config) { c.HistoryDays = days })
		tok, _ := e.tokens()
		access := tok["access_token"].(string)
		today := clock.DateOf(e.srv.cfg.Clock.Now())
		floor := today.AddDays(-days)
		if got := e.srv.HistoryFloor(); got != floor {
			t.Fatalf("%d days: history floor %s, want %s", days, got, floor)
		}
		for _, c := range []struct {
			name string
			d    clock.Date
			want int
		}{
			{"yesterday", today.AddDays(-1), http.StatusOK},
			{"today", today, http.StatusBadRequest},
			{"tomorrow", today.AddDays(1), http.StatusBadRequest},
			{"the day before the floor", floor.AddDays(-1), http.StatusOK},
			{"two days before the floor", floor.AddDays(-2), http.StatusBadRequest},
		} {
			status, body := e.get(e.good, "/accounts/0123456789/balances?as_of="+c.d.String(), access)
			if status != c.want {
				t.Errorf("%d days, %s: as_of %s gave %d %v, want %d", days, c.name, c.d, status, body, c.want)
			}
		}
	}
}
