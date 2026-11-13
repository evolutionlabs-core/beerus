package contracttest

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/connect"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

var createSteps = []string{setup.StepAllocate, setup.StepDirectory, setup.StepDatabase, setup.StepLedgerSchema, setup.StepOpenBankingSchema, setup.StepCerts, setup.StepCBN, setup.StepSite, setup.StepAPI}

func storedStatus(t *testing.T, dir string) setup.Status {
	t.Helper()
	st, err := setup.LoadStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func storedBank(t *testing.T, dir, id string) setup.Resource {
	t.Helper()
	for _, r := range storedStatus(t, dir).Resources {
		if r.Kind == setup.KindBank && r.ID == id {
			return r
		}
	}
	t.Fatalf("no Bank/%s in control.json", id)
	return setup.Resource{}
}

func checkProvisioned(t *testing.T, r setup.Resource) {
	t.Helper()
	if r.Phase != setup.PhaseActive || r.ObservedGeneration != r.Generation || r.Attempts != 0 || r.NextAttemptAt != nil {
		t.Errorf("%s: phase %s generation %d/%d attempts %d next %v", r.ID, r.Phase, r.ObservedGeneration, r.Generation, r.Attempts, r.NextAttemptAt)
	}
	for _, step := range createSteps {
		if r.Steps[step] != "done" {
			t.Errorf("%s: step %s is %q", r.ID, step, r.Steps[step])
		}
	}
}

type run struct {
	app  *connect.App
	stop func()
}

var runOrigins sync.Map

func runOnce(t *testing.T, cfg *config.Config) run {
	t.Helper()
	started, _ := runOrigins.LoadOrStore(cfg.Dir, time.Now())
	origin := started.(time.Time)
	base := stackBase(t)
	app, err := connect.New(cfg, connect.Options{Real: func() time.Time { return base.Add(time.Since(origin)) }})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Serve(ctx) }()
	return run{app: app, stop: func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	}}
}

func TestFreshUpMakesBothBanksActive(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newBankStack(t)
	for _, b := range s.cfg.Banks {
		checkProvisioned(t, storedBank(t, s.cfg.Dir, b.ID))
	}
	for _, st := range s.ctl("GET", "/banks", nil)["data"].([]any) {
		m := st.(map[string]any)
		if m["phase"] != connect.PhaseActive {
			t.Errorf("%v is %v", m["id"], m["phase"])
		}
	}
}

func TestRestartResumesProvisioning(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	cfg, err := initStack(t, "http://127.0.0.1:1/webhooks")
	if err != nil {
		t.Fatal(err)
	}
	first := runOnce(t, cfg)
	first.stop()
	t.Cleanup(func() {
		if _, err := connect.Reset(context.Background(), cfg.Dir, cfg.DatabaseURL); err != nil {
			t.Error(err)
		}
	})
	st := storedStatus(t, cfg.Dir)
	later := time.Now().Add(time.Hour)
	for i := range st.Resources {
		r := &st.Resources[i]
		if r.Kind == setup.KindBank && r.ID == "bank-b" {
			r.Phase = setup.PhaseProvisioning
			r.Steps = map[string]string{setup.StepAllocate: "done", setup.StepDirectory: "done"}
			r.Attempts, r.NextAttemptAt = 3, &later
		}
	}
	if err := setup.SaveStatus(cfg.Dir, st); err != nil {
		t.Fatal(err)
	}
	again, err := connect.LoadConfig(cfg.Dir)
	if err != nil {
		t.Fatal(err)
	}
	second := runOnce(t, again)
	defer second.stop()
	checkProvisioned(t, storedBank(t, cfg.Dir, "bank-b"))
	checkProvisioned(t, storedBank(t, cfg.Dir, "bank-a"))
	for _, b := range second.app.BankStatuses() {
		if b.Phase != connect.PhaseActive {
			t.Errorf("%s is %s after the restart", b.ID, b.Phase)
		}
	}
}

func TestResetDropsEveryDatabase(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	cfg, err := initStack(t, "http://127.0.0.1:1/webhooks")
	if err != nil {
		t.Fatal(err)
	}
	runOnce(t, cfg).stop()
	if err := os.WriteFile(cfg.Path("running"), []byte("now"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := connect.Reset(context.Background(), cfg.Dir, cfg.DatabaseURL); !errors.Is(err, connect.ErrRunning) {
		t.Fatalf("reset while running: %v", err)
	}
	if err := os.Remove(cfg.Path("running")); err != nil {
		t.Fatal(err)
	}
	dropped, err := connect.Reset(context.Background(), cfg.Dir, cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"bank-a", "bank-b", setup.SystemCBN, setup.SystemNIBSS} {
		if !slices.Contains(dropped, config.DatabaseName(cfg.Dir, id)) {
			t.Errorf("%s was not dropped: %v", id, dropped)
		}
	}
	prefix := strings.TrimSuffix(config.DatabaseName(cfg.Dir, "x"), "x")
	left, err := store.List(context.Background(), cfg.DatabaseURL, prefix)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("databases left after reset: %v", left)
	}
	entries, err := os.ReadDir(cfg.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != connect.LocalFile {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("left in the dir: %v", names)
	}
}
