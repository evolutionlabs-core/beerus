package participant

import (
	"context"
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/web"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

type cbnLedger struct {
	mu       sync.Mutex
	posted   map[string]sessionPost
	replayed int
}

func (l *cbnLedger) post(w http.ResponseWriter, r *http.Request) {
	if caller := r.Header.Get(config.CallerHeader); caller != config.NIBSSID {
		web.WriteError(w, r, http.StatusForbidden, "forbidden", caller+" may not post a settlement session. Only NIBSS may.")
		return
	}
	var req sessionPost
	if err := web.DecodeBody(r, &req); err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "Send a session.")
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.posted[req.SessionID]; ok {
		l.replayed++
		w.Header().Set("Idempotent-Replayed", "true")
	} else {
		l.posted[req.SessionID] = req
	}
	web.WriteJSON(w, http.StatusOK, map[string]string{"key": "session:" + req.SessionID})
}

func (l *cbnLedger) snapshot() (map[string]sessionPost, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := map[string]sessionPost{}
	for k, v := range l.posted {
		out[k] = v
	}
	return out, l.replayed
}

func startFakeCBN(t *testing.T, port int) *cbnLedger {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	ledger := &cbnLedger{posted: map[string]sessionPost{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+cbnSessionsURL, ledger.post)
	srv := &http.Server{Handler: web.WithRequestID(mux), ReadHeaderTimeout: time.Second}
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = srv.Serve(ln)
	}()
	t.Cleanup(func() {
		_ = srv.Close()
		<-served
	})
	return ledger
}

const (
	testPortLow  = 20000
	testPortHigh = 31000
)

func freePortBelow32000(t *testing.T) int {
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

func dropDatabase(t *testing.T, url, name string) {
	t.Helper()
	ctx := context.WithoutCancel(t.Context())
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	c, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Errorf("connect to drop %s: %v", name, err)
		return
	}
	defer c.Close(ctx)
	if _, err := c.Exec(ctx, `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`); err != nil {
		t.Errorf("drop %s: %v", name, err)
	}
}

func TestScheduledCutoffPostsItsKeyToTheCBNOnce(t *testing.T) {
	url := os.Getenv("BEERUS_TEST_DATABASE_URL")
	if url == "" || testing.Short() {
		t.Skip("needs Postgres; set BEERUS_TEST_DATABASE_URL and run without -short")
	}
	stage := t.TempDir()
	cbnPort := freePortBelow32000(t)
	cfg := &config.Config{Dir: stage, SetupKey: 9, TSQAfter: time.Minute, Connect: config.Connect{Host: "connect.localhost", BrandName: "Connect", NIBSSPort: 1, NIBSSControlPort: 2, CBNPort: cbnPort, CBNControlPort: 3}}
	if err := config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.EnsureCBNDir(); err != nil {
		t.Fatal(err)
	}
	if err := cfg.EnsureNIBSSDir("wld_test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dropDatabase(t, url, config.DatabaseName(stage, config.NIBSSID)) })
	cbn := startFakeCBN(t, cbnPort)
	ctx := t.Context()
	p, err := New(ctx, stage, Options{DatabaseURL: url, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)

	for id, code := range map[string][2]string{"bank-a": {"000014", "014"}, "bank-b": {"000015", "015"}} {
		m := nibss.Member{Institution: nip.Institution{ID: id, Name: id, NIPCode: code[0], NUBANCode: code[1], Type: "commercial"}, CreditURL: "http://" + id + ".localhost:18451", CallerID: id, Limits: nibss.Limits{NetDebitCap: 500_000_000}}
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
	cutoff := time.Date(2026, time.November, 4, 12, 0, 0, 0, clock.Lagos)
	if n, err := p.settlePass(ctx, cutoff.Add(-time.Minute), false); err != nil || n != 0 {
		t.Fatalf("a pass before the cutoff: %d, %v, want 0", n, err)
	}
	if posted, _ := cbn.snapshot(); len(posted) != 0 {
		t.Fatalf("the CBN received %v before the cutoff", posted)
	}
	if n, err := p.settlePass(ctx, cutoff.Add(time.Hour), false); err != nil || n != 1 {
		t.Fatalf("a pass past the cutoff: %d, %v, want 1", n, err)
	}
	wantNets := []nibss.BankNet{{BankID: "bank-a", Net: -500}, {BankID: "bank-b", Net: 500}}
	posted, replayed := cbn.snapshot()
	got, ok := posted[key]
	if len(posted) != 1 || !ok || !got.CutoffAt.Equal(cutoff) || !reflect.DeepEqual(got.Nets, wantNets) || replayed != 0 {
		t.Fatalf("the CBN received %+v with %d replays, want only %s at %s with nets %v", posted, replayed, key, cutoff, wantNets)
	}
	for k := range posted {
		if strings.HasPrefix(k, "adhoc:") {
			t.Fatalf("a scheduled cutoff was posted as %s", k)
		}
	}
	if n, err := p.store.PostedCount(ctx); err != nil || n != 1 {
		t.Fatalf("posted settlements: %d, %v, want 1", n, err)
	}
	if left, err := p.store.Unposted(ctx); err != nil || len(left) != 0 {
		t.Fatalf("unposted settlements: %v, %v", left, err)
	}

	if err := p.cbn.post(ctx, key, cutoff, wantNets); err != nil {
		t.Fatalf("retry of %s: %v", key, err)
	}
	if n, err := p.settlePass(ctx, cutoff.Add(time.Hour), false); err != nil || n != 0 {
		t.Fatalf("the same pass again: %d, %v, want 0", n, err)
	}
	posted, replayed = cbn.snapshot()
	if len(posted) != 1 || replayed != 1 {
		t.Fatalf("after a retry the CBN holds %d postings and %d replays, want 1 and 1", len(posted), replayed)
	}
	if raw, err := json.Marshal(posted[key].Nets); err != nil || string(raw) != `[{"bank_id":"bank-a","net_minor":-500},{"bank_id":"bank-b","net_minor":500}]` {
		t.Fatalf("posted nets %s, %v", raw, err)
	}
}
