package contracttest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/pki"
)

type obClient struct {
	t      *testing.T
	s      *Stack
	c      *http.Client
	issuer string
}

func (o *obClient) assertion() string {
	o.t.Helper()
	key, err := o.s.cfg.SigningKey()
	if err != nil {
		o.t.Fatal(err)
	}
	jwt, err := openbanking.Sign(key, "connect-1", map[string]any{"iss": o.s.cfg.Connect.RegistryID, "sub": o.s.cfg.Connect.RegistryID, "aud": o.issuer, "jti": fmt.Sprint(time.Now().UnixNano()), "exp": time.Now().Add(time.Minute).Unix()})
	if err != nil {
		o.t.Fatal(err)
	}
	return jwt
}

func (o *obClient) call(req *http.Request) (int, map[string]any) {
	o.t.Helper()
	resp, err := o.c.Do(req)
	if err != nil {
		o.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return resp.StatusCode, out
}

func (o *obClient) post(path string, f url.Values) (int, map[string]any) {
	o.t.Helper()
	f.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
	f.Set("client_assertion", o.assertion())
	req, _ := http.NewRequest("POST", o.issuer+path, strings.NewReader(f.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return o.call(req)
}

func (o *obClient) get(path, token string) (int, map[string]any) {
	o.t.Helper()
	req, _ := http.NewRequest("GET", o.issuer+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return o.call(req)
}

func (s *Stack) mtls(cert, key string) *http.Client {
	s.t.Helper()
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		s.t.Fatal(err)
	}
	return s.mtlsPair(pair)
}

func (s *Stack) mtlsPair(pair tls.Certificate) *http.Client {
	pem, err := os.ReadFile(s.cfg.Path("ca", "root.crt"))
	if err != nil {
		s.t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{pair}, RootCAs: pool}}}
}

func TestOpenBanking(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newBankStack(t)
	snap := s.ctl("POST", "/snapshots", map[string]string{"name": "before consent"})["id"].(string)
	bank := s.cfg.Bank("bank-a")
	o := &obClient{t: t, s: s, issuer: bank.APIURL(), c: s.mtls(s.cfg.Path("connect", "client.crt"), s.cfg.Path("connect", "client.key"))}

	status, meta := o.get("/discover", "")
	if status != 200 || meta["issuer"] != o.issuer || meta["authorization_endpoint"] != bank.URL()+"/authorize" || meta["tls_client_certificate_bound_access_tokens"] != true {
		t.Fatalf("discover: %d %v", status, meta)
	}
	if status, _ := o.get("/health", ""); status != 200 {
		t.Fatalf("health: %d", status)
	}

	verifier := "contract-test-verifier-contract-test-verifier-0123"
	sum := sha256.Sum256([]byte(verifier))
	redirect := s.cfg.WidgetURL() + "/bank/callback"
	push := func() map[string]any {
		status, par := o.post("/par", url.Values{
			"response_type": {"code"}, "client_id": {s.cfg.Connect.RegistryID}, "redirect_uri": {redirect}, "state": {"st-1"},
			"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}, "scope": {"accounts balances transactions"},
			"partner_id": {"balance"}, "partner_name": {"Balance"}, "partner_legal_name": {"Evolution Labs"},
			"purpose": {"to keep your books in sync with this account"}, "transaction_from": {s.today().AddDays(-20).String()},
		})
		if status != http.StatusCreated {
			t.Fatalf("par: %d %v", status, par)
		}
		return par
	}
	par := push()

	b := s.bankBrowser()
	b.c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Host != via[0].URL.Host {
			return http.ErrUseLastResponse
		}
		return nil
	}
	authorize := "/authorize?" + url.Values{"client_id": {s.cfg.Connect.RegistryID}, "request_uri": {par["request_uri"].(string)}}.Encode()
	code := regexp.MustCompile(`verification code is <span[^>]*>(\d{6})</span>`)
	b.get(authorize)
	if b.loc != "/login" {
		t.Fatalf("authorize ended at %s: %.2000s", b.loc, b.last)
	}
	p := s.persona("adaeze")
	b.post("/login", url.Values{"username": {p.Username}, "password": {p.Password}})
	b.post("/verify", url.Values{"otp": {code.FindStringSubmatch(b.last)[1]}})
	if b.loc != "/authorize/consent" {
		t.Fatalf("after 2FA: %s", b.loc)
	}
	for _, want := range []string{"Beerus Connect Limited", s.cfg.Connect.RegistryID, s.cfg.Connect.CACNumber, "Evolution Labs", "to keep your books in sync", "Anonymised analytics", "12 months", "Connected apps"} {
		if !strings.Contains(b.last, want) {
			t.Errorf("consent screen lacks %q", want)
		}
	}
	accounts := regexp.MustCompile(`name="account" value="(\d{10})"`).FindAllStringSubmatch(b.last, -1)
	if len(accounts) < 2 {
		t.Fatalf("consent screen lists %d accounts", len(accounts))
	}
	shared, hidden := accounts[0][1], accounts[1][1]
	b.post("/authorize/consent", url.Values{"action": {"approve"}, "account": {shared}})
	cb, err := url.Parse(b.next)
	if err != nil || !strings.HasPrefix(b.next, redirect+"?") || cb.Query().Get("state") != "st-1" || cb.Query().Get("iss") != o.issuer || cb.Query().Get("code") == "" {
		t.Fatalf("approval redirected to %q", b.next)
	}

	tokenForm := func(c string) url.Values {
		return url.Values{"grant_type": {"authorization_code"}, "code": {c}, "code_verifier": {verifier}, "redirect_uri": {redirect}}
	}
	status, tok := o.post("/token", tokenForm(cb.Query().Get("code")))
	if status != 200 {
		t.Fatalf("token: %d %v", status, tok)
	}
	access, refresh := tok["access_token"].(string), tok["refresh_token"].(string)
	if status, _ := o.post("/token", tokenForm(cb.Query().Get("code"))); status != http.StatusBadRequest {
		t.Fatalf("code reuse: %d", status)
	}
	if status, _ := o.get("/accounts", access); status != http.StatusUnauthorized {
		t.Fatalf("tokens from a reused code should be revoked: %d", status)
	}

	b2 := s.bankBrowser()
	b2.c.CheckRedirect = b.c.CheckRedirect
	par = push()
	b2.get("/login")
	b2.post("/login", url.Values{"username": {p.Username}, "password": {p.Password}})
	b2.post("/verify", url.Values{"otp": {code.FindStringSubmatch(b2.last)[1]}})
	b2.get("/authorize?" + url.Values{"client_id": {s.cfg.Connect.RegistryID}, "request_uri": {par["request_uri"].(string)}}.Encode())
	if !strings.Contains(b2.last, "verification code") {
		t.Fatalf("a signed-in customer was not asked for 2FA again: %.300s", b2.last)
	}
	b2.get("/authorize/consent")
	if strings.Contains(b2.last, "Anonymised analytics") {
		t.Fatal("consent screen shown without a fresh 2FA")
	}
	b2.get("/verify")
	b2.post("/verify", url.Values{"otp": {code.FindStringSubmatch(b2.last)[1]}})
	b2.post("/authorize/consent", url.Values{"action": {"approve"}, "account": {shared}})
	cb2, _ := url.Parse(b2.next)
	status, tok = o.post("/token", tokenForm(cb2.Query().Get("code")))
	if status != 200 {
		t.Fatalf("second token: %d %v", status, tok)
	}
	access, refresh = tok["access_token"].(string), tok["refresh_token"].(string)
	consentID := tok["consent_id"].(string)

	status, list := o.get("/accounts", access)
	data, _ := list["data"].([]any)
	if status != 200 || len(data) != 1 || data[0].(map[string]any)["id"] != shared {
		t.Fatalf("accounts: %d %v", status, list)
	}
	if status, _ := o.get("/accounts/"+hidden+"/balances", access); status != http.StatusForbidden {
		t.Fatalf("account the customer did not share: %d", status)
	}
	status, bal := o.get("/accounts/"+shared+"/balances", access)
	if status != 200 || bal["ledger_balance"] == nil {
		t.Fatalf("balances: %d %v", status, bal)
	}
	status, page := o.get("/accounts/"+shared+"/transactions?page_size=5", access)
	rows, _ := page["data"].([]any)
	if status != 200 || len(rows) != 5 || page["next_cursor"] == nil {
		t.Fatalf("transactions: %d %v", status, page)
	}
	status, page2 := o.get("/accounts/"+shared+"/transactions?page_size=5&cursor="+fmt.Sprint(page["next_cursor"]), access)
	if status != 200 || len(page2["data"].([]any)) == 0 || page2["data"].([]any)[0].(map[string]any)["id"] == rows[0].(map[string]any)["id"] {
		t.Fatalf("second page: %d %v", status, page2)
	}
	if from, _ := page["from"].(string); from < s.today().AddDays(-20).String() {
		t.Fatalf("history starts at %s, before the start date the customer saw", from)
	}

	root, err := pki.LoadAuthority(s.cfg.Path("ca"), "root")
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := root.IssueClient("stranger", "aggregator", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(stranger.CertPEM, stranger.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := (&obClient{t: t, s: s, issuer: o.issuer, c: s.mtlsPair(pair)}).get("/accounts", access); status != http.StatusUnauthorized {
		t.Fatalf("unregistered certificate: %d", status)
	}
	if _, err := s.mtls(s.cfg.Path("partners", "balance", "client.crt"), s.cfg.Path("partners", "balance", "client.key")).Get(o.issuer + "/discover"); err == nil {
		t.Fatal("a certificate from the partner CA reached the bank API")
	}

	b2.get("/apps")
	if !strings.Contains(b2.last, "Balance") || !strings.Contains(b2.last, consentID) {
		t.Fatalf("connected apps page: %.500s", b2.last)
	}
	b2.post("/apps/"+consentID+"/revoke", url.Values{})
	if !strings.Contains(b2.last, "Access withdrawn") {
		t.Fatalf("revoke page: %.300s", b2.last)
	}
	status, body := o.get("/accounts", access)
	if status != http.StatusForbidden || body["error"] != "consent_revoked" {
		t.Fatalf("after revoke: %d %v", status, body)
	}
	if status, body := o.post("/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}}); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("refresh after revoke: %d %v", status, body)
	}

	ledger := s.ctl("GET", "/registry/consents?bank=bank-a", nil)["data"].([]any)
	seen := map[string]bool{}
	for _, e := range ledger {
		m := e.(map[string]any)
		if m["consent_id"] == consentID {
			seen[fmt.Sprint(m["status"])+"/"+fmt.Sprint(m["via"])] = true
		}
	}
	if !seen["Authorised/bank"] || !seen["Revoked/bank"] {
		t.Fatalf("NIBSS consent ledger for %s: %v", consentID, ledger)
	}
	clients := s.ctl("GET", "/registry/clients", nil)["data"].([]any)
	if len(clients) != 1 || clients[0].(map[string]any)["id"] != s.cfg.Connect.RegistryID {
		t.Fatalf("registry: %v", clients)
	}
	newKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := x509.MarshalPKIXPublicKey(&newKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	s.ctl("POST", "/registry/clients", map[string]any{"id": "OBR-T2-000002", "cert_cn": "stranger", "public_key_pem": string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub})), "redirect_uris": []string{"https://other.localhost/cb"}})
	if got := s.ctl("GET", "/registry/clients", nil)["data"].([]any); len(got) != 2 {
		t.Fatalf("registry after add-client: %v", got)
	}
	insecure := map[string]any{"id": "OBR-T2-000003", "cert_cn": "other", "public_key_pem": string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub})), "redirect_uris": []string{"http://other.example.com/cb"}}
	if _, err := s.ctlTry("POST", "/registry/clients", insecure); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("http redirect on a public host: %v", err)
	}
	s.ctl("POST", "/snapshots/"+snap+"/restore", nil)
	if got := s.ctl("GET", "/registry/clients", nil)["data"].([]any); len(got) != 2 {
		t.Fatalf("registry after restore: %v", got)
	}
	kept := map[string]bool{}
	for _, e := range s.ctl("GET", "/registry/consents?bank=bank-a", nil)["data"].([]any) {
		m := e.(map[string]any)
		kept[fmt.Sprint(m["status"])+"/"+fmt.Sprint(m["consent_id"])] = true
	}
	if !kept["Authorised/"+consentID] || !kept["Revoked/"+consentID] {
		t.Fatalf("the consent ledger lost events to a restore: %v", kept)
	}
}

func hasTable(t *testing.T, st *store.Store, name string) bool {
	t.Helper()
	var found *string
	if err := st.Pool.QueryRow(context.Background(), `SELECT to_regclass($1)::text`, name).Scan(&found); err != nil {
		t.Fatal(err)
	}
	return found != nil
}

func TestOpenBankingTablesStayOutOfTheCBN(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newBankStack(t)
	ctx := context.Background()
	for id, want := range map[string]bool{"bank-a": true, "cbn": false} {
		name := s.cfg.Bank("bank-a").Database
		if id == "cbn" {
			name = config.DatabaseName(s.cfg.Dir, id)
		}
		st, err := store.Open(ctx, s.cfg.DatabaseURL, name)
		if err != nil {
			t.Fatal(err)
		}
		if got := hasTable(t, st, "ob_consents"); got != want {
			t.Errorf("%s has ob_consents: %v", id, got)
		}
		st.Close()
	}
	name := config.DatabaseName(s.cfg.Dir, "upgrade")
	st, err := store.Open(ctx, s.cfg.DatabaseURL, name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := st.Drop(ctx); err != nil {
			t.Error(err)
		}
	}()
	if _, err := st.Pool.Exec(ctx, `INSERT INTO goose_db_version (version_id, is_applied) VALUES (3, true)`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = store.Open(ctx, s.cfg.DatabaseURL, name, openbanking.Migrate)
	if err != nil {
		t.Fatalf("a database that already ran the old shared migration: %v", err)
	}
	if !hasTable(t, st, "ob_consents") {
		t.Fatal("no open banking tables after the upgrade")
	}
	st.Close()
}
