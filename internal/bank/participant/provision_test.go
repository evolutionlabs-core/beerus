package participant

import (
	"cmp"
	"context"
	"encoding/json"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"github.com/evolutionlabs-core/beerus/internal/postgres"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

const unreachableURL = "postgres://postgres@127.0.0.1:1/postgres?sslmode=disable"

type stage struct {
	dir           string
	dbURL         string
	token         string
	port, apiPort int
}

func newStage(t *testing.T) *stage {
	t.Helper()
	ctx := context.WithoutCancel(t.Context())
	dbURL := cmp.Or(os.Getenv(config.DatabaseURLEnv), os.Getenv("BEERUS_TEST_DATABASE_URL"), "postgres:///postgres")
	dir := filepath.Join(t.TempDir(), "stage")
	chk(t, os.Mkdir(dir, 0o700))
	cfg := &config.Config{Dir: dir, SetupKey: 1<<63 + 5, Connect: config.Connect{Host: "connect.localhost", BrandName: "Connect", WidgetPort: 17001, ControlPort: 17002, NIBSSPort: 17003}}
	chk(t, config.Prepare(cfg))
	ports := []int{freePort(t), freePort(t)}
	own := &config.Bank{ID: "bank-a", Name: "Bank A", Type: "commercial", NIPCode: "000001", NUBANCode: "001", HistoryDays: 730, Port: ports[0], APIPort: ports[1], Database: config.DatabaseName(dir, "bank-a")}
	chk(t, cfg.EnsureBankDir(own))
	if err := store.Create(ctx, dbURL, config.DatabaseName(dir, "probe")); err != nil {
		t.Skipf("no Postgres for the provisioning test (%v); set BEERUS_TEST_DATABASE_URL", err)
	}
	t.Cleanup(func() {
		_ = postgres.DropDatabases(ctx, dbURL, []string{config.DatabaseName(dir, "probe"), own.Database})
	})
	token, err := config.ReadSecret(filepath.Join(config.BankDir(dir, own.ID), config.BankTokenFile))
	chk(t, err)
	return &stage{dir, dbURL, token, ports[0], ports[1]}
}

func (s *stage) start(t *testing.T, url string) *Participant {
	t.Helper()
	p, err := New(t.Context(), s.dir, Options{BankID: "bank-a", DatabaseURL: url, Bind: "127.0.0.1"})
	chk(t, err)
	t.Cleanup(p.Close)
	return p
}

func (s *stage) status(t *testing.T, p *Participant) control.State {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	r.RemoteAddr = "127.0.0.1:1"
	r.Header.Set("Authorization", "Bearer "+s.token)
	w := httptest.NewRecorder()
	p.Server().API().ServeHTTP(w, r)
	var st control.State
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &st) != nil {
		t.Fatalf("status answered %d %s", w.Code, w.Body)
	}
	return st
}

type stepRow struct {
	id, name string
	done     bool
	failed   bool
}

func rows(st control.State) []stepRow {
	out := []stepRow{}
	for _, s := range st.Steps {
		out = append(out, stepRow{s.ID, s.Name, s.Done, s.Error != ""})
	}
	return out
}

func sameRows(got, want []stepRow) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func steps(db, ledger, ob stepRow) []stepRow {
	return []stepRow{
		{"allocate", "Allocating codes (NIP 000001, NUBAN 001)", true, false},
		{"directory", "Registering with NIBSS", true, false},
		db, ledger, ob,
		{"files", "Writing the bank files", true, false},
		{"cbn", "Opening the CBN settlement account", true, false},
		{"site", "Starting online banking", true, false},
		{"api", "Starting the open banking API", true, false},
	}
}

func TestBankProvisionsItsOwnDatabase(t *testing.T) {
	t.Parallel()
	s := newStage(t)
	first := s.start(t, s.dbURL)
	if st := s.status(t, first); st.Phase != config.PhaseProvisioning || st.Steps[2].Done {
		t.Fatalf("before the first ensure: phase %s, steps %+v", st.Phase, rows(st))
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		first.Ensure(t.Context())
	}()
	for finished := false; !finished; {
		s.status(t, first)
		select {
		case <-done:
			finished = true
		default:
		}
	}
	st := s.status(t, first)
	migrations := st.Steps[3].Name
	if !strings.HasPrefix(migrations, "Migrating bank ledger (") || strings.Contains(migrations, "no new") {
		t.Fatalf("a fresh database migrated nothing: %q", migrations)
	}
	want := steps(stepRow{"database", "Creating the bank database", true, false}, stepRow{"ledger_schema", migrations, true, false}, stepRow{"openbanking_schema", st.Steps[4].Name, true, false})
	if !sameRows(rows(st), want) || st.Phase != config.PhaseActive || st.Reason != "" {
		t.Fatalf("first start: phase %s %q, steps %+v", st.Phase, st.Reason, rows(st))
	}
	for _, step := range st.Steps[2:5] {
		if step.StartedAt == nil || step.CompletedAt == nil || step.CompletedAt.Before(*step.StartedAt) {
			t.Fatalf("step %s has times %v and %v", step.ID, step.StartedAt, step.CompletedAt)
		}
	}
	first.Close()
	second := s.start(t, s.dbURL)
	second.Ensure(t.Context())
	st = s.status(t, second)
	want = steps(stepRow{"database", "Creating the bank database", true, false}, stepRow{"ledger_schema", "Migrating bank ledger (no new migrations)", true, false}, stepRow{"openbanking_schema", "Migrating open banking (no new migrations)", true, false})
	if !sameRows(rows(st), want) || st.Phase != config.PhaseActive {
		t.Fatalf("second start: phase %s, steps %+v", st.Phase, rows(st))
	}
}

func TestFailedStepRetriesWithBackoff(t *testing.T) {
	t.Parallel()
	s := newStage(t)
	p := s.start(t, unreachableURL)
	p.ensureDue(t.Context())
	st := s.status(t, p)
	failing := steps(stepRow{"database", "Creating the bank database", false, true}, stepRow{"ledger_schema", "Migrating bank ledger", false, false}, stepRow{"openbanking_schema", "Migrating open banking", false, false})
	if !sameRows(rows(st), failing) || st.Phase != config.PhaseProvisioning || st.Reason != "Step database failed. Trying again." {
		t.Fatalf("first failure: phase %s %q, steps %+v", st.Phase, st.Reason, rows(st))
	}
	if msg := st.Steps[2].Error; !strings.HasPrefix(msg, "cannot reach Postgres for the bank ledger") {
		t.Fatalf("step error %q", msg)
	}
	ctx := t.Context()
	p.ensureDue(ctx)
	if p.attempts != 1 {
		t.Fatalf("a retry before the backoff ran: %d attempts", p.attempts)
	}
	p.retryAt = p.clock.Real().Add(time.Second)
	p.ensureDue(ctx)
	if p.attempts != 1 {
		t.Fatalf("a retry a second before the backoff ends ran: %d attempts", p.attempts)
	}
	p.retryAt = p.clock.Real()
	before := p.clock.Real()
	p.ensureDue(ctx)
	after := p.clock.Real()
	if p.attempts != 2 || p.retryAt.Before(before.Add(clock.Backoff(2))) || p.retryAt.After(after.Add(clock.Backoff(2))) {
		t.Fatalf("after the first backoff: %d attempts, next at %v, want %v after the attempt", p.attempts, p.retryAt, clock.Backoff(2))
	}
	p.cfg.DatabaseURL = s.dbURL
	p.retryAt = p.clock.Real()
	p.ensureDue(ctx)
	st = s.status(t, p)
	if st.Phase != config.PhaseActive || st.Reason != "" || p.attempts != 0 || p.booksNow() == nil {
		t.Fatalf("after recovery: phase %s %q, %d attempts, steps %+v", st.Phase, st.Reason, p.attempts, rows(st))
	}
	for _, step := range st.Steps {
		if !step.Done || step.Error != "" {
			t.Fatalf("step %+v after recovery", step)
		}
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	chk(t, err)
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func (s *stage) get(t *testing.T, port int, path string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+path, nil)
	chk(t, err)
	req.Header.Set("Authorization", "Bearer "+s.token)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	chk(t, err)
	return resp.StatusCode, strings.TrimSpace(string(raw))
}

func TestSetupPageGivesWayToTheBankWithoutLeavingThePortUnserved(t *testing.T) {
	t.Parallel()
	s := newStage(t)
	own := struct{ Port, APIPort int }{s.port, s.apiPort}
	p := s.start(t, s.dbURL)
	ctx := t.Context()
	chk(t, p.start(ctx))
	t.Cleanup(func() { p.stop(context.WithoutCancel(ctx)) })
	if status, body := s.get(t, own.Port, "/health"); status != http.StatusServiceUnavailable || body != "Bank A is setting up its database. Try again in a minute." {
		t.Fatalf("setup page: %d %q", status, body)
	}
	if status, _ := s.get(t, own.Port, "/v1/status"); status != http.StatusOK {
		t.Fatalf("status while setting up: %d", status)
	}
	p.Ensure(ctx)
	blocker, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(own.APIPort))
	chk(t, err)
	if p.promote(ctx) {
		t.Fatal("the bank started on a port that is taken")
	}
	if status, _ := s.get(t, own.Port, "/v1/status"); status != http.StatusOK {
		t.Fatalf("status after the failed swap: %d", status)
	}
	blocker.Close()
	if !p.promote(ctx) {
		t.Fatal("the bank did not start once the port was free")
	}
	if status, body := s.get(t, own.Port, "/health"); status != http.StatusOK || body != "ok" {
		t.Fatalf("health after the swap: %d %q", status, body)
	}
	if !p.promote(ctx) {
		t.Fatal("a second promote is not a no-op")
	}
}

func (s *stage) setPhase(t *testing.T, p *Participant, phase string) {
	t.Helper()
	body := `{"phase":"` + phase + `","generation":5,"world_id":"wld_1"}`
	r := httptest.NewRequest(http.MethodPut, "/v1/phase", strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:1"
	r.Header.Set("Authorization", "Bearer "+s.token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	p.Server().API().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("set phase answered %d %s", w.Code, w.Body)
	}
}

func TestBankRebuildsADroppedDatabase(t *testing.T) {
	t.Parallel()
	s := newStage(t)
	p := s.start(t, s.dbURL)
	ctx := t.Context()
	name := p.cfg.Banks[0].Database
	drop := func() {
		t.Helper()
		chk(t, postgres.DropDatabases(ctx, s.dbURL, []string{name}))
	}
	p.Ensure(ctx)
	if p.booksLost(ctx) {
		t.Fatal("a healthy database was reported lost")
	}
	drop()
	if !p.booksLost(ctx) {
		t.Fatal("a dropped database was not reported lost")
	}
	p.forgetBooks()
	if _, err := p.Server().Books(); err == nil {
		t.Fatal("the closed books were still handed out")
	}
	st := s.status(t, p)
	if st.Phase != config.PhaseActive || st.Steps[2].Done || st.Steps[3].Done || st.Steps[4].Done || st.Steps[2].CompletedAt != nil {
		t.Fatalf("after the loss: phase %s, steps %+v", st.Phase, rows(st))
	}
	p.Ensure(ctx)
	st = s.status(t, p)
	if st.Phase != config.PhaseActive || !st.Steps[2].Done || !st.Steps[3].Done || !st.Steps[4].Done || st.Steps[2].CompletedAt == nil || p.booksLost(ctx) {
		t.Fatalf("after the rebuild: phase %s, steps %+v", st.Phase, rows(st))
	}

	p.forgetBooks()
	p.steps[2].Done = true
	drop()
	p.ensureDue(ctx)
	st = s.status(t, p)
	if st.Steps[2].Done || st.Steps[3].Done || st.Steps[3].Error == "" {
		t.Fatalf("after the failed step: steps %+v", rows(st))
	}
	p.retryAt = p.clock.Real()
	p.ensureDue(ctx)
	st = s.status(t, p)
	if !st.Steps[2].Done || !st.Steps[3].Done || !st.Steps[4].Done || p.booksLost(ctx) {
		t.Fatalf("after the retry: steps %+v", rows(st))
	}

	drop()
	p.forgetBooks()
	s.setPhase(t, p, config.PhaseClosed)
	p.Ensure(ctx)
	st = s.status(t, p)
	if st.Steps[2].Done || st.Steps[3].Done || st.Steps[4].Done || st.Steps[2].Error != "the bank is closed, so its database is left alone" {
		t.Fatalf("a closed bank rebuilt: %+v, step error %q", rows(st), st.Steps[2].Error)
	}
	if got, err := postgres.List(ctx, s.dbURL, name); err != nil || len(got) != 0 {
		t.Fatalf("databases of a closed bank: %v, %v", got, err)
	}
}

func chk(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
