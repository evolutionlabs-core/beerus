package participant

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pb33f/libopenapi"
	validator "github.com/pb33f/libopenapi-validator"

	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/pki"
)

const (
	testPortLow  = 20000
	testPortHigh = 31000
	testWait     = 10 * time.Second
	testPoll     = 20 * time.Millisecond
	specPath     = "../../../api/settlement/openapi.yaml"
)

func freePort(t *testing.T) int {
	t.Helper()
	for range 200 {
		port := testPortLow + rand.IntN(testPortHigh-testPortLow)
		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil {
			ln.Close()
			return port
		}
	}
	t.Fatal("no free port")
	return 0
}

type call struct {
	who, method, path, body string
	status                  int
	reply                   string
	replayed                bool
}

func TestCBNServesSettlement(t *testing.T) {
	url := os.Getenv("BEERUS_TEST_DATABASE_URL")
	if url == "" || testing.Short() {
		t.Skip("needs Postgres; set BEERUS_TEST_DATABASE_URL and run without -short")
	}
	now := clock.New(nil).Now()
	stage := t.TempDir()
	cfg := &config.Config{Dir: stage, SetupKey: 9, Connect: config.Connect{Host: "connect.localhost", BrandName: "Connect", NIBSSPort: freePort(t), NIBSSControlPort: freePort(t), CBNPort: freePort(t), CBNControlPort: freePort(t)}}
	if err := config.Prepare(cfg, now); err != nil {
		t.Fatal(err)
	}
	if err := cfg.EnsureCBNDir(now); err != nil {
		t.Fatal(err)
	}
	if err := cfg.EnsureNIBSSDir("wld_test", now); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"bank-a", "bank-b", "bank-c"} {
		b := &config.Bank{ID: id, Name: id, Type: "commercial", Port: 3 + 2*i, APIPort: 4 + 2*i, Database: config.DatabaseName(stage, id)}
		if err := cfg.EnsureBankDir(b, now); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if err := store.DropDatabases(context.WithoutCancel(t.Context()), url, []string{config.DatabaseName(stage, config.CBNID)}); err != nil {
			t.Errorf("drop the CBN database: %v", err)
		}
	})
	ctx, stop := context.WithCancel(context.WithoutCancel(t.Context()))
	p, err := New(ctx, stage, Options{DatabaseURL: url})
	if err != nil {
		stop()
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- p.Serve(ctx) }()
	t.Cleanup(func() {
		stop()
		if err := <-served; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
	token, err := config.ReadSecret(filepath.Join(config.CBNDir(stage), config.CBNTokenFile))
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pki.LoadPool(filepath.Join(config.CBNDir(stage), config.CBNCAFile))
	if err != nil {
		t.Fatal(err)
	}
	mutual := func(dir, name string) *http.Client {
		pair, err := tls.LoadX509KeyPair(filepath.Join(dir, name+".crt"), filepath.Join(dir, name+".key"))
		if err != nil {
			t.Fatal(err)
		}
		return &http.Client{Timeout: testWait, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{pair}, ServerName: config.CBNHost, MinVersion: tls.VersionTLS12}}}
	}
	clients := map[string]*http.Client{
		"nibss":  mutual(config.NIBSSDir(stage), "client"),
		"bank-a": mutual(filepath.Join(stage, "banks", "bank-a"), "client"),
		"bank-b": mutual(filepath.Join(stage, "banks", "bank-b"), "client"),
		"bank-c": mutual(filepath.Join(stage, "banks", "bank-c"), "client"),
	}
	spec, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := libopenapi.NewDocument(spec)
	if err != nil {
		t.Fatal(err)
	}
	v, errs := validator.NewValidator(doc)
	if len(errs) > 0 {
		t.Fatalf("validator: %v", errs)
	}
	control := "http://127.0.0.1:" + strconv.Itoa(cfg.Connect.CBNControlPort)
	api := "https://127.0.0.1:" + strconv.Itoa(cfg.Connect.CBNPort)
	do := func(c call) (int, string, http.Header) {
		t.Helper()
		base, client := api, clients[c.who]
		if c.who == "token" || c.who == "anonymous" {
			base, client = control, &http.Client{Timeout: testWait}
		}
		req, err := http.NewRequestWithContext(ctx, c.method, base+c.path, strings.NewReader(c.body))
		if err != nil {
			t.Fatal(err)
		}
		if c.who == "token" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, err.Error(), nil
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			t.Fatal(err)
		}
		check := resp.Request.Clone(context.WithoutCancel(ctx))
		check.Body = io.NopCloser(strings.NewReader(c.body))
		vresp := *resp
		vresp.Body = io.NopCloser(bytes.NewReader(raw))
		if ok, errs := v.ValidateHttpResponse(check, &vresp); !ok && strings.HasPrefix(c.path, "/v1/") && !strings.HasPrefix(c.path, "/v1/health") && !strings.HasPrefix(c.path, "/v1/status") {
			for _, e := range errs {
				t.Errorf("%s %s: %s %s", c.method, c.path, e.Message, e.Reason)
			}
		}
		return resp.StatusCode, string(raw), resp.Header
	}
	up, giveUp := context.WithTimeout(ctx, testWait)
	defer giveUp()
	tick := time.NewTicker(testPoll)
	defer tick.Stop()
	for status, _, _ := do(call{who: "token", method: http.MethodGet, path: "/v1/health"}); status != http.StatusOK; status, _, _ = do(call{who: "token", method: http.MethodGet, path: "/v1/health"}) {
		select {
		case <-up.Done():
			t.Fatal("the CBN never answered")
		case <-tick.C:
		}
	}
	account := func(id string) string { return "/v1/control/banks/" + id + "/settlement-account" }
	session := func(id string, a, b int) string {
		return fmt.Sprintf(`{"session_id":%q,"cutoff_at":"2100-01-02T12:00:00+01:00","nets":[{"bank_id":"bank-a","net_minor":%d},{"bank_id":"bank-b","net_minor":%d}]}`, id, a, b)
	}
	calls := []call{
		{"anonymous", http.MethodPut, account("bank-a"), `{"settlement_minor":1000,"vault_minor":200}`, 401, `"code":"unauthenticated"`, false},
		{"token", http.MethodPut, account("bank-a"), `{"settlement_minor":1000,"vault_minor":200}`, 200, `"bank_id":"bank-a","settlement_minor":1000,"vault_minor":200`, false},
		{"token", http.MethodPut, account("bank-a"), `{"settlement_minor":1000,"vault_minor":200}`, 200, `"bank_id":"bank-a","settlement_minor":1000,"vault_minor":200`, false},
		{"token", http.MethodPut, account("bank-b"), `{"settlement_minor":1000,"vault_minor":0}`, 200, `"bank_id":"bank-b","settlement_minor":1000,"vault_minor":0`, false},
		{"token", http.MethodPut, account("bank-a"), `{"settlement_minor":999,"vault_minor":200}`, 422, "bank-a opened with settlement_minor 1000 and vault_minor 200, and this request has settlement_minor 999. Send the amounts it opened with.", false},
		{"token", http.MethodPut, account("bank-a"), `{"settlement_minor":1000}`, 400, `"code":"invalid_request"`, false},
		{"token", http.MethodPut, account("bank-c"), `{"settlement_minor":0,"vault_minor":0}`, 400, `"code":"invalid_request"`, false},
		{"token", http.MethodGet, "/v1/status", "", 200, `"kind":"cbn","phase":"active"`, false},
		{"token", http.MethodGet, "/v1/status", "", 200, `"problems":[]`, false},
		{"nibss", http.MethodPost, "/v1/settlement-sessions", session("s1", -250, 250), 200, `"key":"session:s1","kind":"session","session_id":"s1","narration":"NIP settlement session s1"`, false},
		{"nibss", http.MethodPost, "/v1/settlement-sessions", session("s1", -250, 250), 200, `"key":"session:s1"`, true},
		{"nibss", http.MethodPost, "/v1/settlement-sessions", session("s1", -300, 300), 422, "Session s1 was posted with bank-a -250 and bank-b 250, and this request has bank-a -300 and bank-b 300. Send a new session_id, or repeat the first nets.", false},
		{"nibss", http.MethodPost, "/v1/settlement-sessions", session("s2", -100, 250), 422, `"code":"settlement_unbalanced","message":"The session nets to 150, not zero. Send nets that sum to zero."`, false},
		{"nibss", http.MethodPost, "/v1/settlement-sessions", `{"session_id":"s3","cutoff_at":"2100-01-02T12:00:00+01:00","nets":[{"bank_id":"bank-a","net_minor":-5},{"bank_id":"bank-a","net_minor":5}]}`, 422, `"code":"duplicate_bank","message":"bank-a appears 2 times in session s3. Send one net per bank."`, false},
		{"nibss", http.MethodPost, "/v1/settlement-sessions", `{"session_id":"s4","cutoff_at":"2100-01-02T12:00:00+01:00","nets":[{"bank_id":"bank-a","net_minor":-5},{"bank_id":"bank-x","net_minor":5}]}`, 422, `"code":"unknown_bank"`, false},
		{"nibss", http.MethodPost, "/v1/settlement-sessions", `{"session_id":"s5","cutoff_at":"2100-01-02T12:00:00+01:00","nets":[{"bank_id":"bank-a","net_minor":9223372036854775807},{"bank_id":"bank-b","net_minor":1}]}`, 422, `"code":"amount_too_large","message":"The nets of session s5 are too large to add up. The largest net is 9223372036854775807 kobo. Send smaller nets."`, false},
		{"nibss", http.MethodPost, "/v1/settlement-sessions", `{"session_id":"s5b","cutoff_at":"2100-01-02T12:00:00+01:00","nets":[{"bank_id":"bank-a","net_minor":-9223372036854775807},{"bank_id":"bank-b","net_minor":9223372036854775807}]}`, 422, `"code":"amount_too_large","message":"Session s5b would take bank-b's settlement balance past the largest amount: 1250 + 9223372036854775807. Send smaller nets."`, false},
		{"nibss", http.MethodPost, "/v1/settlement-sessions", `{"session_id":"s6","cutoff_at":"2100-01-02T12:00:00+01:00","nets":[{"bank_id":"bank-a","net_minor":0}]}`, 400, `"code":"invalid_request"`, false},
		{"bank-a", http.MethodPost, "/v1/settlement-sessions", session("s7", -5, 5), 403, `"code":"forbidden"`, false},
		{"bank-a", http.MethodGet, "/v1/banks/bank-a/settlement-balance", "", 200, `"bank_id":"bank-a","balance_minor":750,"read_at":`, false},
		{"bank-a", http.MethodGet, "/v1/banks/bank-b/settlement-balance", "", 403, "This certificate may not read the balance of bank-b.", false},
		{"nibss", http.MethodGet, "/v1/banks/bank-a/settlement-balance", "", 403, `"code":"forbidden"`, false},
		{"bank-c", http.MethodGet, "/v1/banks/bank-c/settlement-balance", "", 404, "No settlement account for that bank.", false},
		{"bank-b", http.MethodGet, "/v1/banks/bank-b/settlement-balance", "", 200, `"balance_minor":1250`, false},
		{"bank-a", http.MethodGet, "/v1/settlement-postings?limit=1", "", 200, `"next_cursor":"session:s1"`, false},
		{"bank-a", http.MethodGet, "/v1/settlement-postings?limit=5&cursor=session:s1", "", 200, `"key":"genesis:bank-a","kind":"genesis"`, false},
		{"bank-a", http.MethodGet, "/v1/settlement-postings?bank_id=bank-b", "", 403, `"code":"forbidden"`, false},
		{"bank-c", http.MethodGet, "/v1/settlement-postings", "", 200, `"data":[]`, false},
		{"nibss", http.MethodGet, "/v1/settlement-postings?limit=2", "", 200, `"next_cursor":`, false},
		{"nibss", http.MethodGet, "/v1/settlement-postings?bank_id=bank-b&limit=500", "", 200, `"key":"genesis:bank-b"`, false},
		{"nibss", http.MethodGet, "/v1/settlement-postings?cursor=session:nope", "", 400, `"code":"invalid_cursor","message":"That cursor is not valid. Use the next_cursor value from the last page."`, false},
		{"nibss", http.MethodGet, "/v1/settlement-postings?limit=0", "", 400, `"code":"invalid_request"`, false},
		{"nibss", http.MethodGet, "/v1/settlement-postings?limit=501", "", 400, `"code":"invalid_request"`, false},
	}
	first := ""
	for i, c := range calls {
		status, body, header := do(c)
		if status != c.status || !strings.Contains(body, c.reply) || (header.Get(replayedHeader) == "true") != c.replayed {
			t.Errorf("call %d %s %s: %d %s replayed=%q, want %d containing %s replayed=%v", i, c.method, c.path, status, body, header.Get(replayedHeader), c.status, c.reply, c.replayed)
		}
		switch i {
		case 1:
			first = body
		case 2:
			if body != first {
				t.Errorf("a second opening answered %s, want %s", body, first)
			}
		}
	}
	if !p.begin("s9") {
		t.Fatal("a new session was already in flight")
	}
	if status, body, _ := do(call{who: "nibss", method: http.MethodPost, path: "/v1/settlement-sessions", body: session("s9", -5, 5)}); status != http.StatusConflict || !strings.Contains(body, `"code":"session_in_flight","message":"Session s9 is still being posted. Send it again in a moment."`) {
		t.Errorf("a session in flight: %d %s", status, body)
	}
	p.end("s9")
	if status, body, _ := do(call{who: "nibss", method: http.MethodPost, path: "/v1/settlement-sessions", body: session("s9", -5, 5)}); status != http.StatusOK {
		t.Errorf("a session after its first post ended: %d %s", status, body)
	}
	anon := &http.Client{Timeout: testWait, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: config.CBNHost, MinVersion: tls.VersionTLS12}}}
	if resp, err := anon.Get(api + "/v1/settlement-postings"); err == nil {
		resp.Body.Close()
		t.Errorf("a call without a client certificate got %d", resp.StatusCode)
	}
	if _, err := New(ctx, stage, Options{}); err == nil || err.Error() != config.DatabaseURLEnv+" is not set; set it to the Postgres URL of the CBN" {
		t.Errorf("start without a database: %v", err)
	}
}
