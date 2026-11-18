package participant

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	cbnparticipant "github.com/evolutionlabs-core/beerus/internal/cbn/participant"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/pki"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

const (
	settleTestPortLow  = 20000
	settleTestPortHigh = 31000
	settleTestWait     = 10 * time.Second
	settleTestPoll     = 20 * time.Millisecond
)

func settleFreePort(t *testing.T) int {
	t.Helper()
	for range 200 {
		port := settleTestPortLow + rand.IntN(settleTestPortHigh-settleTestPortLow)
		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil {
			ln.Close()
			return port
		}
	}
	t.Fatal("no free port")
	return 0
}

func TestScheduledCutoffPostsItsKeyToTheCBNOnce(t *testing.T) {
	url := os.Getenv("BEERUS_TEST_DATABASE_URL")
	if url == "" || testing.Short() {
		t.Skip("needs Postgres; set BEERUS_TEST_DATABASE_URL and run without -short")
	}
	now := clock.New().Now()
	stage := t.TempDir()
	cfg := &config.Config{Dir: stage, SetupKey: 9, TSQAfter: time.Minute, Connect: config.Connect{Host: "connect.localhost", BrandName: "Connect", NIBSSPort: settleFreePort(t), NIBSSControlPort: settleFreePort(t), CBNPort: settleFreePort(t), CBNControlPort: settleFreePort(t)}}
	if err := config.Prepare(cfg, now); err != nil {
		t.Fatal(err)
	}
	if err := cfg.EnsureCBNDir(now); err != nil {
		t.Fatal(err)
	}
	if err := cfg.EnsureNIBSSDir("wld_test", now); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		names := []string{config.DatabaseName(stage, config.CBNID), config.DatabaseName(stage, config.NIBSSID)}
		if err := store.DropDatabases(context.WithoutCancel(t.Context()), url, names); err != nil {
			t.Errorf("drop the databases: %v", err)
		}
	})
	ctx, stop := context.WithCancel(context.WithoutCancel(t.Context()))
	cbnp, err := cbnparticipant.New(ctx, stage, cbnparticipant.Options{DatabaseURL: url, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		stop()
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- cbnp.Serve(ctx) }()
	t.Cleanup(func() {
		stop()
		if err := <-served; err != nil {
			t.Errorf("serve the CBN: %v", err)
		}
	})
	p, err := New(ctx, stage, Options{DatabaseURL: url, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)

	token, err := config.ReadSecret(filepath.Join(config.CBNDir(stage), config.CBNTokenFile))
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pki.LoadPool(filepath.Join(config.CBNDir(stage), config.CBNCAFile))
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(filepath.Join(config.NIBSSDir(stage), "client.crt"), filepath.Join(config.NIBSSDir(stage), "client.key"))
	if err != nil {
		t.Fatal(err)
	}
	nibssClient := &http.Client{Timeout: settleTestWait, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{pair}, ServerName: config.CBNHost, MinVersion: tls.VersionTLS12}}}
	control := "http://127.0.0.1:" + strconv.Itoa(cfg.Connect.CBNControlPort)
	api := "https://127.0.0.1:" + strconv.Itoa(cfg.Connect.CBNPort)
	do := func(client *http.Client, method, url, auth, body string) (int, string, http.Header) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
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
		return resp.StatusCode, string(raw), resp.Header
	}
	plain := &http.Client{Timeout: settleTestWait}
	up, giveUp := context.WithTimeout(ctx, settleTestWait)
	defer giveUp()
	tick := time.NewTicker(settleTestPoll)
	defer tick.Stop()
	for status, _, _ := do(plain, http.MethodGet, control+"/v1/health", token, ""); status != http.StatusOK; status, _, _ = do(plain, http.MethodGet, control+"/v1/health", token, "") {
		select {
		case <-up.Done():
			t.Fatal("the CBN never answered")
		case <-tick.C:
		}
	}
	for _, id := range []string{"bank-a", "bank-b"} {
		if status, body, _ := do(plain, http.MethodPut, control+"/v1/control/banks/"+id+"/settlement-account", token, `{"settlement_minor":1000,"vault_minor":0}`); status != http.StatusOK {
			t.Fatalf("open %s at the CBN: %d %s", id, status, body)
		}
	}

	for id, code := range map[string][2]string{"bank-a": {"000014", "014"}, "bank-b": {"000015", "015"}} {
		m := nibss.Member{Institution: nip.Institution{ID: id, Name: id, NIPCode: code[0], NUBANCode: code[1], Type: "commercial"}, CreditURL: "https://" + id + ".localhost:18451", CertificateIdentity: id, Limits: nibss.Limits{NetDebitCap: 500_000_000}}
		if err := p.store.Register(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	resolved := time.Date(2026, time.November, 4, 10, 30, 0, 0, clock.Lagos)
	x := nibss.Session{ID: "sched1", FromCode: "000014", ToCode: "000015", FromNUBAN: world.NUBAN("014", 1), ToNUBAN: world.NUBAN("015", 1), Amount: 500, Narration: "rent", CreatedAt: resolved.Add(-time.Minute)}
	if _, _, err := p.store.Begin(ctx, x); err != nil {
		t.Fatal(err)
	}
	if err := p.store.Resolve(ctx, x.ID, nibss.SessionOK, nip.OutcomeCredited, resolved); err != nil {
		t.Fatal(err)
	}

	const key = "2026-11-04T12:00"
	pastCutoff := time.Date(2026, time.November, 4, 13, 0, 0, 0, clock.Lagos)
	if n, err := p.settlePass(ctx, pastCutoff.Add(-2*time.Hour), false); err != nil || n != 0 {
		t.Fatalf("a pass before the cutoff: %d, %v", n, err)
	}
	if n, err := p.settlePass(ctx, pastCutoff, false); err != nil || n != 1 {
		t.Fatalf("a pass past the cutoff: %d, %v, want 1 settlement", n, err)
	}
	if n, err := p.store.PostedCount(ctx); err != nil || n != 1 {
		t.Fatalf("posted settlements: %d, %v, want 1", n, err)
	}
	if left, err := p.store.Unposted(ctx); err != nil || len(left) != 0 {
		t.Fatalf("unposted settlements: %v, %v", left, err)
	}
	status, postings, _ := do(nibssClient, http.MethodGet, api+"/v1/settlement-postings?bank_id=bank-a", "", "")
	if status != http.StatusOK || !strings.Contains(postings, `"key":"session:`+key+`"`) || strings.Contains(postings, "adhoc:") || strings.Count(postings, `"kind":"session"`) != 1 {
		t.Fatalf("postings of bank-a: %d %s, want one session posting with key session:%s", status, postings, key)
	}
	sessionPostings := func() int {
		t.Helper()
		status, body, _ := do(nibssClient, http.MethodGet, api+"/v1/settlement-postings?limit=500", "", "")
		if status != http.StatusOK {
			t.Fatalf("list postings: %d %s", status, body)
		}
		return strings.Count(body, `"kind":"session"`)
	}
	if n := sessionPostings(); n != 1 {
		t.Fatalf("session postings at the CBN: %d, want 1", n)
	}

	retry := fmt.Sprintf(`{"session_id":%q,"cutoff_at":"2026-11-04T12:00:00+01:00","nets":[{"bank_id":"bank-a","net_minor":-500},{"bank_id":"bank-b","net_minor":500}]}`, key)
	status, body, header := do(nibssClient, http.MethodPost, api+"/v1/settlement-sessions", "", retry)
	if status != http.StatusOK || header.Get("Idempotent-Replayed") != "true" || !strings.Contains(body, `"key":"session:`+key+`"`) {
		t.Fatalf("retry of the same key: %d replayed=%q %s, want 200 replayed=true", status, header.Get("Idempotent-Replayed"), body)
	}
	if n, err := p.settlePass(ctx, pastCutoff, false); err != nil || n != 0 {
		t.Fatalf("the same pass again: %d, %v, want 0", n, err)
	}
	if n := sessionPostings(); n != 1 {
		t.Fatalf("session postings at the CBN after the retries: %d, want 1", n)
	}
}
