package connect

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/evolutionlabs-core/beerus/internal/bank/banktest"
	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/install"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/state"
)

func testStore(t *testing.T) *state.Store {
	t.Helper()
	s, err := state.Create(t.TempDir(), setup.Status{Version: setup.StatusVersion, Setup: "beerus", SetupKey: 9})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func initTestConfig(t *testing.T, today clock.Date) *config.Config {
	t.Helper()
	local := install.DefaultLocal()
	ports := testPorts(t, 2)
	local.Connect.PartnerPort, local.Connect.WidgetPort, local.Connect.ControlPort, local.Connect.HealthPort, local.Connect.NIBSSPort, local.Connect.CBNPort = ports[0], ports[1], ports[2], ports[3], ports[4], ports[5]
	return initConfigWith(t, today, local)
}

func initConfigWith(t *testing.T, today clock.Date, local setup.Local) *config.Config {
	t.Helper()
	dir := t.TempDir()
	if u := os.Getenv("BEERUS_TEST_DATABASE_URL"); u != "" {
		local.DatabaseURL = u
	}
	if err := install.Init(dir, banktest.Spec("beerus"), local, today); err != nil {
		t.Fatal(err)
	}
	cfg, err := state.LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

const (
	testPortLow  = 20000
	testPortHigh = 32000
)

func testPorts(t *testing.T, banks int) []int {
	t.Helper()
	need := 7 + banks
	for range 200 {
		base := testPortLow + rand.IntN(testPortHigh-testPortLow-need-config.DefaultAPIPortOffset)
		free := true
		for p := base; p < base+need && free; p++ {
			free = config.PortFree(p) && (p < base+7 || config.PortFree(p+config.DefaultAPIPortOffset))
		}
		if free {
			return []int{base, base + 1, base + 2, base + 3, base + 4, base + 5, base + 6}
		}
	}
	t.Fatal("no free block of ports for the app")
	return nil
}

const (
	testProvisionLimit = 30 * time.Second
	testProvisionPoll  = 50 * time.Millisecond
)

const testBindTries = 4

func startTestNIBSS(t *testing.T, o Options) (*App, context.Context, func(), error) {
	t.Helper()
	cfg := initTestConfig(t, clock.Date{Y: 2026, M: 11, D: 3})
	conn, err := pgx.Connect(context.Background(), cfg.DatabaseURL)
	if err != nil {
		t.Skipf("no Postgres for app tests (%v); set BEERUS_TEST_DATABASE_URL", err)
	}
	conn.Close(context.Background())
	a, err := New(t.Context(), cfg, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.DropBankDatabases(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("drop databases: %v", err)
		}
	})
	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	errc := make(chan error, 1)
	done, err := a.serveNIBSS(ctx, errc)
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}
	cbnDone, err := a.serveCBN(ctx, errc)
	if err != nil {
		cancel()
		<-done
		return nil, nil, nil, err
	}
	stop := sync.OnceFunc(func() {
		cancel()
		<-done
		<-cbnDone
	})
	t.Cleanup(stop)
	tick := time.NewTicker(testProvisionPoll)
	defer tick.Stop()
	upCtx, upStop := context.WithTimeout(ctx, testProvisionLimit)
	defer upStop()
	for !participantsAnswer(upCtx, a) {
		select {
		case <-upCtx.Done():
			stop()
			return nil, nil, nil, upCtx.Err()
		case err := <-errc:
			stop()
			return nil, nil, nil, err
		case <-tick.C:
		}
	}
	return a, ctx, stop, nil
}

func newTestApp(t *testing.T, o Options) *App {
	t.Helper()
	a, _ := newTestAppWithNIBSS(t, o)
	return a
}

func newTestAppWithNIBSS(t *testing.T, o Options) (*App, func()) {
	t.Helper()
	if testing.Short() {
		t.Skip("opens bank databases; run without -short")
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	var a *App
	var ctx context.Context
	var stopNIBSS func()
	for try := 0; ; try++ {
		var err error
		if a, ctx, stopNIBSS, err = startTestNIBSS(t, o); err == nil {
			break
		}
		if try == testBindTries {
			t.Fatal(err)
		}
	}
	waitCtx, stop := context.WithTimeout(ctx, testProvisionLimit)
	defer stop()
	tick := time.NewTicker(testProvisionPoll)
	defer tick.Stop()
	for !participantsAnswer(waitCtx, a) || provisioning(a) {
		select {
		case <-waitCtx.Done():
			t.Fatalf("banks still provisioning: %+v", a.BankStatuses())
		case <-tick.C:
		}
		a.Step(waitCtx)
	}
	for _, b := range a.cfg.Banks {
		api, err := a.startBankAPI(b)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = api.Close() })
	}
	return a, stopNIBSS
}

func participantsAnswer(ctx context.Context, a *App) bool {
	return answers(ctx, a.nibssCtl.link, a.nibssCtl.base) && answers(ctx, a.cbnCtl.link, a.cbnCtl.base)
}

func answers(ctx context.Context, link server.Link, base string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/health", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+link.Token)
	resp, err := link.Client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func provisioning(a *App) bool {
	return slices.ContainsFunc(a.BankStatuses(), func(st BankStatus) bool { return st.Phase == PhaseProvisioning })
}

func TestWriteFilesRestoresExactlyTheSavedSet(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "partner")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"api.key": "old", "client.key": "old", "stale.crt": "old"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeFiles(dir, map[string][]byte{"api.key": []byte("new"), "client.crt": []byte("cert")}); err != nil {
		t.Fatal(err)
	}
	got, err := readFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || string(got["api.key"]) != "new" || string(got["client.crt"]) != "cert" {
		t.Fatalf("restored files: %v", got)
	}
	info, err := os.Stat(filepath.Join(dir, "api.key"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v %v", info, err)
	}
	if err := writeFiles(filepath.Join(dir, "api.key", "x"), map[string][]byte{"a": nil}); err == nil {
		t.Fatal("a restore under a file did not fail")
	}
}

func TestClockIsReal(t *testing.T) {
	app := newTestApp(t, Options{})
	before := time.Now().Truncate(time.Millisecond)
	got := app.clock.Now()
	after := time.Now()
	if got.Before(before) || got.After(after) || got.Location() != clock.Lagos {
		t.Fatalf("clock now %v, want between %v and %v in Lagos", got, before, after)
	}
	if app.setupKey == 0 || app.setupKey != app.ctl.SetupKey() {
		t.Fatalf("setup key %d", app.setupKey)
	}
}
