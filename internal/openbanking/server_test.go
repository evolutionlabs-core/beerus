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

const (
	verifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	allScopes = "accounts balances transactions"
	redirect  = "http://connect.localhost/cb"
	day       = 24 * time.Hour
)

type fakeRegistry struct{ client *Client }

func (r fakeRegistry) Client(id string) (*Client, bool) { return r.client, id == r.client.ID }

func (r fakeRegistry) ClientByCaller(id string) (*Client, bool) {
	return r.client, id == r.client.CertCN
}

type fakeData struct{}

func (fakeData) Accounts(context.Context, string) ([]AccountInfo, error) {
	return []AccountInfo{{ID: "0123456789", Name: "Current", Type: "current", Currency: "NGN"}, {ID: "0123456790", Name: "Savings", Type: "savings", Currency: "NGN"}}, nil
}

func (fakeData) Account(_ context.Context, id string) (AccountInfo, error) {
	return AccountInfo{ID: id, Name: "Current", Type: "current", Currency: "NGN"}, nil
}

func (fakeData) Balance(context.Context, string) (Balance, error) {
	return Balance{Ledger: 5000, Available: 4000, Currency: "NGN"}, nil
}

func (fakeData) BalanceOn(context.Context, string, clock.Date) (int64, error) { return 4200, nil }

func (fakeData) Transactions(context.Context, string, clock.Date, clock.Date) ([]Txn, error) {
	d, _ := clock.ParseDate("2026-01-05")
	out := []Txn{}
	for i := 0; i < 5; i++ {
		out = append(out, Txn{ID: string(rune('a' + i)), Amount: int64(i), ValueDate: d, Seq: int64(i), BookedAt: time.Now()})
	}
	return out, nil
}

func (fakeData) Ping(context.Context) error { return nil }

type env struct {
	t     *testing.T
	srv   *Server
	ts    *httptest.Server
	store *MemStore
	key   *ecdsa.PrivateKey
	good  *http.Client
}

type callerTransport struct{ id string }

func (c callerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set(config.CallerHeader, c.id)
	return http.DefaultTransport.RoundTrip(r)
}

func clientFor(id string) *http.Client { return &http.Client{Transport: callerTransport{id}} }

func newEnv(t *testing.T, tweaks ...func(*Config)) *env {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	chk(t, err)
	e := &env{t: t, store: NewMemStore(), key: key, good: clientFor("connect")}
	var h http.Handler
	e.ts = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) }))
	e.ts.Start()
	t.Cleanup(e.ts.Close)
	client := &Client{ID: "OBR-1", Name: "Connect", Role: "aggregator", CertCN: "connect", Keys: []*ecdsa.PublicKey{&key.PublicKey}, RedirectURIs: []string{redirect}}
	cfg := Config{BankID: "bank-a", Terms: Terms{BankName: "Bank A"}, Issuer: e.ts.URL, AuthorizeURL: "http://bank-a.localhost/authorize", HistoryDays: 90, Store: e.store, Registry: fakeRegistry{client}, Data: fakeData{}}
	for _, tweak := range tweaks {
		tweak(&cfg)
	}
	e.srv = New(cfg)
	h = e.srv.Handler()
	return e
}

func (e *env) lastLedger() ConsentEvent {
	e.t.Helper()
	due, err := e.srv.LedgerDue(context.Background(), 100)
	if err != nil || len(due) == 0 {
		e.t.Fatalf("ledger events %+v: %v", due, err)
	}
	return due[len(due)-1].ConsentEvent
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

func (e *env) sign(key *ecdsa.PrivateKey, aud string, life time.Duration) string {
	e.t.Helper()
	jwt, err := Sign(key, "k1", map[string]any{"iss": "OBR-1", "sub": "OBR-1", "aud": aud, "jti": random("j", 8), "exp": time.Now().Add(life).Unix(), "iat": time.Now().Unix()})
	if err != nil {
		e.t.Fatal(err)
	}
	return jwt
}

func (e *env) do(req *http.Request, c *http.Client) (int, map[string]any) {
	e.t.Helper()
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (e *env) post(c *http.Client, path string, f url.Values) (int, map[string]any) {
	e.t.Helper()
	if f.Get("client_assertion") == "" {
		f.Set("client_assertion", e.sign(e.key, e.ts.URL, time.Minute))
		f.Set("client_assertion_type", assertionType)
	}
	req, err := http.NewRequest(http.MethodPost, e.ts.URL+path, strings.NewReader(f.Encode()))
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return e.do(req, c)
}

func (e *env) get(c *http.Client, path, token string) (int, map[string]any) {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodGet, e.ts.URL+path, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return e.do(req, c)
}

func (e *env) refresh(token string) (int, map[string]any) {
	return e.post(e.good, "/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {token}})
}

func challenge() string {
	sum := sha256.Sum256([]byte(verifier))
	return b64.EncodeToString(sum[:])
}

func (e *env) pushed(scope string) (string, string) {
	e.t.Helper()
	status, body := e.post(e.good, "/par", url.Values{
		"response_type": {"code"}, "client_id": {"OBR-1"}, "redirect_uri": {redirect}, "state": {"st1"},
		"code_challenge": {challenge()}, "code_challenge_method": {"S256"}, "partner_id": {"balance"}, "partner_name": {"Balance"},
		"transaction_from": {"2026-02-01"}, "scope": {scope},
	})
	if status != http.StatusCreated {
		e.t.Fatalf("par: %d %v", status, body)
	}
	return body["request_uri"].(string), body["consent_id"].(string)
}

func (e *env) code(scope string) (string, string) {
	e.t.Helper()
	uri, id := e.pushed(scope)
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
	return e.post(c, "/token", url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {v}, "redirect_uri": {redirect}})
}

func (e *env) tokens(scope string) (map[string]any, string) {
	e.t.Helper()
	code, id := e.code(scope)
	status, body := e.exchange(e.good, code, verifier)
	if status != http.StatusOK {
		e.t.Fatalf("token: %d %v", status, body)
	}
	return body, id
}

func (e *env) getIs(c *http.Client, path, token string, status int, code string) map[string]any {
	e.t.Helper()
	got, body := e.get(c, path, token)
	if got != status || (code != "" && body["error"] != code) {
		e.t.Fatalf("GET %s: %d %v, want %d %s", path, got, body, status, code)
	}
	return body
}

func (e *env) exchangeIs(c *http.Client, code, v string, status int, errCode string) map[string]any {
	e.t.Helper()
	got, body := e.exchange(c, code, v)
	if got != status || (errCode != "" && body["error"] != errCode) {
		e.t.Fatalf("exchange: %d %v, want %d %s", got, body, status, errCode)
	}
	return body
}

func (e *env) refreshIs(token string, status int, errCode string) map[string]any {
	e.t.Helper()
	got, body := e.refresh(token)
	if got != status || (errCode != "" && body["error"] != errCode) {
		e.t.Fatalf("refresh: %d %v, want %d %s", got, body, status, errCode)
	}
	return body
}

func (e *env) revoke(id string) {
	e.t.Helper()
	if err := e.srv.Revoke(context.Background(), id, "adaeze", "bank"); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) consent(id string) *Consent {
	e.t.Helper()
	c, err := e.store.Consent(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

func TestFlow(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	tok, id := e.tokens(allScopes)
	access := tok["access_token"].(string)
	if body := e.getIs(e.good, "/accounts", access, 200, ""); len(body["data"].([]any)) != 1 {
		t.Fatalf("accounts %v", body)
	}
	if body := e.getIs(e.good, "/accounts/0123456789/balances", access, 200, ""); body["ledger_balance"].(map[string]any)["amount_minor"].(float64) != 5000 {
		t.Fatalf("balances %v", body)
	}
	e.getIs(e.good, "/accounts/0123456790/balances", access, http.StatusForbidden, "")
	if body := e.getIs(e.good, "/discover", "", 200, ""); body["token_endpoint"] != e.ts.URL+"/token" {
		t.Fatalf("discover %v", body)
	}
	seen, cursors, cursor := []string{}, []string{}, ""
	for i := 0; i < 5 && (i == 0 || cursor != ""); i++ {
		body := e.getIs(e.good, "/accounts/0123456789/transactions?page_size=2&cursor="+url.QueryEscape(cursor), access, 200, "")
		for _, row := range body["data"].([]any) {
			seen = append(seen, row.(map[string]any)["id"].(string))
		}
		cursor, _ = body["next_cursor"].(string)
		cursors = append(cursors, cursor)
	}
	if strings.Join(seen, "") != "abcde" || cursors[0] != "2026-01-05.1" {
		t.Fatalf("paged through %v with cursors %v", seen, cursors)
	}
	for _, bad := range []string{"2026-01-05.99999999999999999999", "x.1", "2026-01-05"} {
		e.getIs(e.good, "/accounts/0123456789/transactions?cursor="+url.QueryEscape(bad), access, http.StatusBadRequest, "")
	}
	if body := e.getIs(e.good, "/accounts/0123456789/transactions?cursor=2099-01-01.0", access, 200, ""); len(body["data"].([]any)) != 0 || body["next_cursor"] != nil {
		t.Errorf("cursor past the end: %v", body)
	}
	want := clock.DateOf(time.Now()).AddDays(defaultLookaheadDays).String()
	if body := e.getIs(e.good, "/accounts/0123456789/transactions", access, 200, ""); body["to"] != want {
		t.Errorf("default window to %v, want %s", body["to"], want)
	}
	if body := e.getIs(e.good, "/accounts/0123456789/transactions?to=2026-01-06", access, 200, ""); body["to"] != "2026-01-06" {
		t.Errorf("requested window to %v", body["to"])
	}
	c, _ := e.srv.Consent(ctx, id)
	if hist, _ := e.srv.AccessHistory(ctx, "adaeze", 10); c.Status != Authorised || c.Customer != "adaeze" || c.LastUsedAt.IsZero() || len(hist) == 0 {
		t.Fatalf("consent %+v with %d history rows", c, len(hist))
	}
	for k, v := range map[string]string{"access": access, "refresh": tok["refresh_token"].(string)} {
		if stored, err := e.store.Token(ctx, Hash(v)); err != nil || stored.Kind != k || stored.Hash == v {
			t.Fatalf("%s token is not stored as a hash", k)
		}
	}
}

func TestScopeNarrowsWhatIsShared(t *testing.T) {
	e := newEnv(t)
	tok, id := e.tokens("balances accounts")
	access := tok["access_token"].(string)
	e.getIs(e.good, "/accounts/0123456789/balances", access, 200, "")
	e.getIs(e.good, "/accounts/0123456789/transactions", access, http.StatusForbidden, "insufficient_scope")
	if got := strings.Join(e.consent(id).Scopes, " "); got != "accounts balances" || tok["scope"] != got {
		t.Fatalf("stored scopes %q, token scope %v", got, tok["scope"])
	}
}

func TestPushedRequests(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Terms.AssertionLife = time.Minute })
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	chk(t, err)
	replayed := e.sign(e.key, e.ts.URL, 30*time.Second)
	rows := []struct {
		name   string
		set    map[string]string
		status int
		code   string
	}{
		{"plain PKCE", map[string]string{"code_challenge_method": "plain"}, 400, ""},
		{"no scope", map[string]string{"scope": ""}, 400, "invalid_scope"},
		{"junk assertion", map[string]string{"client_assertion": "junk"}, 401, ""},
		{"signed by another key", map[string]string{"client_assertion": e.sign(other, e.ts.URL, 30*time.Second)}, 401, ""},
		{"endpoint as audience", map[string]string{"client_assertion": e.sign(e.key, e.ts.URL+"/par", 30*time.Second)}, 401, ""},
		{"issuer as audience", map[string]string{"client_assertion": replayed}, 201, ""},
		{"replayed assertion", map[string]string{"client_assertion": replayed}, 401, ""},
	}
	for _, r := range rows {
		f := url.Values{"response_type": {"code"}, "redirect_uri": {redirect}, "code_challenge": {challenge()}, "code_challenge_method": {"S256"}, "scope": {allScopes}}
		for k, v := range r.set {
			f.Set(k, v)
		}
		if f.Get("client_assertion") != "" {
			f.Set("client_assertion_type", assertionType)
		}
		status, body := e.post(e.good, "/par", f)
		if status != r.status || (r.code != "" && body["error"] != r.code) {
			t.Errorf("%s: %d %v, want %d %s", r.name, status, body, r.status, r.code)
		}
	}
}

func TestAuthorisationCodes(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	uri, _ := e.pushed(allScopes)
	if _, err := e.srv.Request(ctx, uri, "someone-else"); err == nil {
		t.Fatal("request_uri worked for another client")
	}
	if _, err := e.srv.Approve(ctx, uri, "adaeze", nil, false); err != ErrNoAccounts {
		t.Fatalf("approve without accounts: %v", err)
	}
	_, err := e.srv.Approve(ctx, uri, "adaeze", []string{"0123456789"}, false)
	chk(t, err)
	if _, err := e.srv.Approve(ctx, uri, "adaeze", []string{"0123456789"}, false); err == nil {
		t.Fatal("request_uri was approved twice")
	}
	uri, id := e.pushed(allScopes)
	loc, err := e.srv.Reject(ctx, uri)
	chk(t, err)
	u, _ := url.Parse(loc)
	if u.Query().Get("error") != "access_denied" || u.Query().Get("iss") != e.ts.URL || e.consent(id).Status != Rejected {
		t.Fatalf("redirect %s", loc)
	}
	code, _ := e.code(allScopes)
	e.exchangeIs(e.good, code, "wrong-verifier-wrong-verifier-wrong-verifier-1", http.StatusBadRequest, "invalid_grant")
	e.exchangeIs(e.good, code, verifier, http.StatusBadRequest, "")
	code, _ = e.code(allScopes)
	access := e.exchangeIs(e.good, code, verifier, 200, "")["access_token"].(string)
	e.exchangeIs(e.good, code, verifier, http.StatusBadRequest, "")
	e.getIs(e.good, "/accounts", access, http.StatusUnauthorized, "")
	code, _ = e.code(allScopes)
	e.ageRequests(time.Second, time.Second)
	if body := e.exchangeIs(e.good, code, verifier, http.StatusBadRequest, ""); !strings.Contains(body["error_description"].(string), "expired") {
		t.Fatalf("expired code: %v", body)
	}
}

func TestWrongCaller(t *testing.T) {
	e := newEnv(t)
	code, _ := e.code(allScopes)
	for _, r := range []struct {
		name   string
		client *http.Client
		desc   string
	}{
		{"no caller", &http.Client{}, "Send your ID in the Beerus-Caller header."},
		{"unregistered ID", clientFor("stranger"), "stranger is not registered at the Registry."},
	} {
		if body := e.exchangeIs(r.client, code, verifier, http.StatusUnauthorized, "invalid_client"); body["error_description"] != r.desc {
			t.Errorf("%s: %v, want %q", r.name, body, r.desc)
		}
	}
	access := e.exchangeIs(e.good, code, verifier, 200, "")["access_token"].(string)
	e.getIs(&http.Client{}, "/accounts", access, http.StatusUnauthorized, "invalid_client")
}

func TestRefreshTokens(t *testing.T) {
	e := newEnv(t)
	tok, _ := e.tokens(allScopes)
	first := tok["refresh_token"].(string)
	second := e.refreshIs(first, 200, "")
	if second["refresh_token"] == first {
		t.Fatal("refresh token was not rotated")
	}
	third := e.refreshIs(first, 200, "")
	e.getIs(e.good, "/accounts", second["access_token"].(string), http.StatusUnauthorized, "")
	e.editToken(first, func(t *Token) { t.RotatedAt = time.Now().Add(-50 * time.Second) })
	e.refreshIs(first, 200, "")
	e.editToken(first, func(t *Token) { t.RotatedAt = time.Now().Add(-70 * time.Second) })
	e.refreshIs(first, http.StatusBadRequest, "invalid_grant")
	e.refreshIs(third["refresh_token"].(string), http.StatusBadRequest, "")
	tok, _ = e.tokens(allScopes)
	access, refresh := tok["access_token"].(string), tok["refresh_token"].(string)
	past := time.Now().Add(-time.Second)
	e.editToken(access, func(x *Token) { x.ExpiresAt = past })
	e.getIs(e.good, "/accounts", access, http.StatusUnauthorized, "invalid_token")
	e.editToken(refresh, func(x *Token) { x.ExpiresAt = past })
	e.refreshIs(refresh, http.StatusBadRequest, "invalid_grant")
}

func TestConsentLifecycle(t *testing.T) {
	rows := []struct {
		name  string
		tweak func(*Config)
		act   func(*env, string)
		code  string
		want  Status
	}{
		{"revoked by the customer", nil, func(e *env, id string) { e.revoke(id) }, "consent_revoked", Revoked},
		{"a year has passed", nil, func(e *env, id string) {
			e.editConsent(id, func(c *Consent) { c.ExpiresAt = time.Now().Add(-time.Second) })
		}, "consent_reconsent_required", ReconsentRequired},
		{"unused for 31 days with a 30 day setting", func(c *Config) { c.Terms.DormantDays = 30 }, func(e *env, id string) {
			e.editConsent(id, func(c *Consent) { c.AuthorisedAt, c.LastUsedAt = time.Now().Add(-31*day), time.Time{} })
		}, "consent_dormant", Dormant},
	}
	for _, r := range rows {
		var tweaks []func(*Config)
		if r.tweak != nil {
			tweaks = append(tweaks, r.tweak)
		}
		e := newEnv(t, tweaks...)
		tok, id := e.tokens(allScopes)
		before := e.srv.cfg.Clock.Now()
		e.getIs(e.good, "/accounts", tok["access_token"].(string), 200, "")
		if used := e.consent(id); used.LastUsedAt.Before(before) || used.LastUsedAt.After(e.srv.cfg.Clock.Now()) {
			t.Fatalf("%s: last used %s", r.name, used.LastUsedAt)
		}
		r.act(e, id)
		e.getIs(e.good, "/accounts", tok["access_token"].(string), http.StatusForbidden, r.code)
		if c := e.consent(id); c.Status != r.want {
			t.Errorf("%s: status %s, want %s", r.name, c.Status, r.want)
		}
		e.refreshIs(tok["refresh_token"].(string), http.StatusBadRequest, "")
		if last := e.lastLedger(); last.Status != r.want || last.ConsentID != id {
			t.Errorf("%s: ledger %+v", r.name, last)
		}
	}
}

func TestRevokeNeedsTheCustomer(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	_, id := e.tokens(allScopes)
	for _, who := range []string{"someone-else", ""} {
		if err := e.srv.Revoke(ctx, id, who, "bank"); err != ErrNotYours {
			t.Fatalf("revoke by %q: %v", who, err)
		}
	}
	if c := e.consent(id); c.Status != Authorised {
		t.Fatalf("status %s", c.Status)
	}
	e.revoke(id)
	if last := e.lastLedger(); last.Via != "bank" {
		t.Fatalf("ledger %+v", last)
	}
}

func TestTerms(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, func(c *Config) { c.Terms.ConsentMonths, c.Terms.AccessLife, c.Terms.RefreshDays = 6, 3*time.Minute, 10 })
	before := time.Now()
	tok, id := e.tokens(allScopes)
	after := time.Now()
	c, _ := e.store.Consent(ctx, id)
	if want := addMonths(c.AuthorisedAt, 6); !c.ExpiresAt.Equal(want) {
		t.Fatalf("consent expires %s, want %s", c.ExpiresAt, want)
	}
	for kind, raw := range map[string]string{"access": tok["access_token"].(string), "refresh": tok["refresh_token"].(string)} {
		stored, err := e.store.Token(ctx, Hash(raw))
		chk(t, err)
		min, max := before.Add(3*time.Minute), after.Add(3*time.Minute)
		if kind == "refresh" {
			min, max = before.AddDate(0, 0, 10), after.AddDate(0, 0, 10)
		}
		if stored.ExpiresAt.Before(min) || stored.ExpiresAt.After(max) {
			t.Errorf("%s token expires %s, want between %s and %s", kind, stored.ExpiresAt, min, max)
		}
	}
	uri, _ := e.pushed(allScopes)
	lo := e.srv.cfg.Clock.Now()
	req, err := e.srv.Request(ctx, uri, "OBR-1")
	hi := e.srv.cfg.Clock.Now()
	if err != nil || req.Months != 6 || req.Until.Before(addMonths(lo, 6)) || req.Until.After(addMonths(hi, 6)) || tok["expires_in"] != float64(180) {
		t.Fatalf("request %+v %v, expires_in %v", req, err, tok["expires_in"])
	}
	e.srv.SetTerms(Terms{BankName: "Bank B", ConsentMonths: 9, AccessLife: 20 * time.Minute})
	if _, body := e.get(e.good, "/discover", ""); body["name"] != "Bank B" {
		t.Fatalf("discover name %v", body["name"])
	}
	if req, err := e.srv.Request(ctx, uri, "OBR-1"); err != nil || req.Months != 9 {
		t.Fatalf("pending request %+v %v", req, err)
	}
	if tok, _ := e.tokens(allScopes); tok["expires_in"] != float64(1200) {
		t.Errorf("expires_in %v, want 1200", tok["expires_in"])
	}
	e.srv.SetTerms(Terms{})
	if req, err := e.srv.Request(ctx, uri, "OBR-1"); err != nil || req.Months != DefaultConsentMonths {
		t.Fatalf("pending request %+v %v", req, err)
	}
	if tok, _ := e.tokens(allScopes); tok["expires_in"] != float64(600) {
		t.Errorf("default expires_in %v, want 600", tok["expires_in"])
	}
}

func TestLifetimeSettingsAreClamped(t *testing.T) {
	long := Config{CodeLife: time.Minute, RequestLife: 10 * time.Minute, RefreshGrace: time.Minute}
	short := Config{CodeLife: 20 * time.Second, RequestLife: 2 * time.Minute, RefreshGrace: 5 * time.Second}
	for _, c := range []struct {
		name     string
		in, want Config
	}{
		{"defaults", Config{}, long},
		{"too long", Config{CodeLife: time.Hour, RequestLife: time.Hour}, long},
		{"shorter is kept", short, short},
	} {
		got := New(c.in).cfg
		if got.CodeLife != c.want.CodeLife || got.RequestLife != c.want.RequestLife || got.RefreshGrace != c.want.RefreshGrace {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
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
		{at(2026, time.August, 31), 6, at(2027, time.February, 28)},
		{at(2026, time.January, 31), 1, at(2026, time.February, 28)},
		{at(2026, time.March, 15), 12, at(2027, time.March, 15)},
	} {
		if got := addMonths(c.from, c.months); !got.Equal(c.want) {
			t.Errorf("%s + %d months = %s, want %s", c.from.Format("2006-01-02"), c.months, got.Format("2006-01-02"), c.want.Format("2006-01-02"))
		}
	}
}

func TestConsentStatus(t *testing.T) {
	authorised := time.Date(2026, time.March, 1, 9, 0, 0, 0, time.UTC)
	monthEnd := time.Date(2026, time.August, 31, 9, 0, 0, 0, time.UTC)
	at := func(days int) time.Time { return authorised.AddDate(0, 0, days) }
	consent := func(status Status, from, expires, lastUsed time.Time) *Consent {
		return &Consent{Status: status, AuthorisedAt: from, ExpiresAt: expires, LastUsedAt: lastUsed}
	}
	year := addMonths(authorised, 12)
	none := time.Time{}
	for _, c := range []struct {
		name    string
		consent *Consent
		now     time.Time
		dormant int
		want    Status
	}{
		{"fresh", consent(Authorised, authorised, year, none), authorised, 180, Authorised},
		{"day 180 unused", consent(Authorised, authorised, year, none), at(180), 180, Dormant},
		{"unused 180 days after the last use", consent(Authorised, authorised, year, at(179)), at(359), 180, Dormant},
		{"day before the year ends", consent(Authorised, authorised, year, at(364)), at(364), 400, Authorised},
		{"expiry beats dormancy", consent(Authorised, authorised, year, none), at(366), 180, ReconsentRequired},
		{"revoked stays revoked", consent(Revoked, authorised, year, none), at(1), 180, Revoked},
		{"month end, at the expiry", consent(Authorised, monthEnd, addMonths(monthEnd, 6), none), addMonths(monthEnd, 6), 400, ReconsentRequired},
	} {
		if got := consentStatus(c.consent, c.now, c.dormant); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestTimeWindows(t *testing.T) {
	at := time.Date(2026, time.March, 1, 9, 0, 0, 0, time.UTC)
	life := time.Minute
	today, floor := clock.Date{Y: 2026, M: time.March, D: 1}, clock.Date{Y: 2025, M: time.December, D: 1}
	for _, c := range []struct {
		name      string
		got, want bool
	}{
		{"lapsed at the instant", lapsed(at, at), false},
		{"lapsed a nanosecond after", lapsed(at.Add(time.Nanosecond), at), true},
		{"assertion expires now", assertionWindow(at, at, life), true},
		{"assertion past the window", assertionWindow(at.Add(life+time.Second), at, life), false},
		{"as of today", asOfAllowed(today, today, floor), false},
		{"as of the floor", asOfAllowed(floor, today, floor), true},
		{"as of the day before the floor", asOfAllowed(floor.AddDays(-1), today, floor), true},
	} {
		if c.got != c.want {
			t.Errorf("%s: %v, want %v", c.name, c.got, c.want)
		}
	}
	if got := expiresAt(at, 10*time.Minute); !got.Equal(at.Add(10 * time.Minute)) {
		t.Errorf("expires %s", got)
	}
}

func TestBalanceAsOfFollowsTheHistoryWindow(t *testing.T) {
	for _, days := range []int{1, 366} {
		e := newEnv(t, func(c *Config) { c.HistoryDays = days })
		tok, _ := e.tokens(allScopes)
		today := clock.DateOf(e.srv.cfg.Clock.Now())
		floor := today.AddDays(-days)
		if got := e.srv.HistoryFloor(); got != floor {
			t.Fatalf("%d days: history floor %s, want %s", days, got, floor)
		}
		for d, want := range map[clock.Date]int{today.AddDays(-1): 200, today: 400, today.AddDays(1): 400, floor.AddDays(-1): 200, floor.AddDays(-2): 400} {
			e.getIs(e.good, "/accounts/0123456789/balances?as_of="+d.String(), tok["access_token"].(string), want, "")
		}
	}
}

type failingStore struct{ *MemStore }

func (failingStore) RecordAccess(context.Context, Access) error { return errors.New("disk full") }

func TestRequestsAndOutbox(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	_, stale := e.pushed(allScopes)
	code, _ := e.code(allScopes)
	e.ageRequests(11*time.Minute, 5*time.Minute)
	_, lost := e.pushed(allScopes)
	e.srv.mu.Lock()
	pars, codes := len(e.srv.pars), len(e.srv.codes)
	e.srv.mu.Unlock()
	if pars != 1 || codes != 1 || e.consent(stale).Status != Expired {
		t.Fatalf("%d requests and %d codes kept, abandoned request is %s", pars, codes, e.consent(stale).Status)
	}
	e.ageRequests(0, 11*time.Minute)
	e.exchangeIs(e.good, code, verifier, http.StatusBadRequest, "")
	e.srv.mu.Lock()
	codes = len(e.srv.codes)
	e.srv.mu.Unlock()
	if codes != 0 {
		t.Fatalf("%d codes kept", codes)
	}
	chk(t, New(e.srv.cfg).Resume(ctx))
	if e.consent(lost).Status != Expired {
		t.Fatalf("status %s", e.consent(lost).Status)
	}

	e = newEnv(t)
	tok, id := e.tokens(allScopes)
	e.revoke(id)
	due, err := e.srv.LedgerDue(ctx, 10)
	if err != nil || len(due) != 2 || due[0].Status != Authorised || due[1].Status != Revoked || due[0].ID >= due[1].ID {
		t.Fatalf("pending ledger events %+v: %v", due, err)
	}
	chk(t, e.srv.Retry(ctx, due[0], time.Now().Add(time.Minute)))
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
		chk(t, e.srv.Delivered(ctx, d))
	}
	if left, _ := e.srv.LedgerDue(ctx, 10); len(left) != 0 {
		t.Fatalf("delivered events still due: %+v", left)
	}

	e = newEnv(t)
	tok, _ = e.tokens(allScopes)
	e.srv.cfg.Store = failingStore{e.store}
	if body := e.getIs(e.good, "/accounts/0123456789/balances", tok["access_token"].(string), http.StatusInternalServerError, ""); body["data"] != nil {
		t.Fatalf("balances with a broken access log: %v", body)
	}
}

func chk(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
