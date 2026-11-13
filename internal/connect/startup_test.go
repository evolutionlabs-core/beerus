package connect

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/banktest"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func TestOldDirIsRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, oldSpecFile), []byte("seed: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"load":  func() error { _, err := LoadConfig(dir); return err }(),
		"init":  Init(dir, banktest.Spec("beerus", clock.Date{Y: 2026, M: 11, D: 3}), DefaultLocal(), clock.Date{Y: 2026, M: 11, D: 3}),
		"check": func() error { _, err := Initialized(dir); return err }(),
	} {
		if !errors.Is(err, ErrOldSetup) || !strings.Contains(err.Error(), "This setup was made by an older Beerus. Run beerus reset, then beerus init.") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, statusFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an old dir got a status store: %v", err)
	}
}

func TestInitWritesTheDefaultSetup(t *testing.T) {
	today := clock.Date{Y: 2026, M: 11, D: 3}
	cfg := initTestConfig(t, today)
	for _, f := range []string{SpecFile, LocalFile, statusFile, "control-token"} {
		fi, err := os.Stat(cfg.Path(f))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s has mode %v", f, fi.Mode().Perm())
		}
	}
	raw, err := os.ReadFile(cfg.Path(SpecFile))
	if err != nil {
		t.Fatal(err)
	}
	spec, errs := setup.Parse(SpecFile, raw)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if spec.Spec.Start != "2026-11-03T00:00:00+01:00" || len(spec.Spec.Banks) != 2 {
		t.Fatalf("setup.yaml: %+v", spec.Spec)
	}
	want := []struct {
		id, typ, nip, nuban string
		port, api           int
	}{{"bank-a", "commercial", "999001", "901", 7445, 7455}, {"bank-b", "digital", "999002", "902", 7446, 7456}}
	for i, w := range want {
		b := cfg.Banks[i]
		if b.ID != w.id || b.Type != w.typ || b.NIPCode != w.nip || b.NUBANCode != w.nuban || b.Port != w.port || b.APIPort != w.api || b.Database == "" {
			t.Errorf("bank %d: %+v", i, b)
		}
		if b.HistoryDays != typeHistoryDays[w.typ] {
			t.Errorf("%s history %d", b.ID, b.HistoryDays)
		}
	}
	if !cfg.Start.Equal(time.Date(2026, 11, 3, 0, 0, 0, 0, clock.Lagos)) {
		t.Errorf("start %v", cfg.Start)
	}
	st, err := setup.LoadStatus(cfg.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.SetupKey == 0 || st.Setup != "beerus" || st.AppliedSHA256 == "" {
		t.Fatalf("status: key %d setup %q sum %q", st.SetupKey, st.Setup, st.AppliedSHA256)
	}
	if st.Applied.Spec.Banks[0].NIPCode != "999001" {
		t.Errorf("applied codes are not pinned: %+v", st.Applied.Spec.Banks[0])
	}
	r := findResource(&st, setup.KindBank, "bank-b")
	if r == nil || r.Phase != setup.PhaseProvisioning || r.Steps[setup.StepAllocate] != stepDone || r.Generation != 1 {
		t.Fatalf("bank-b resource: %+v", r)
	}
	for _, id := range []string{setup.SystemCBN, setup.SystemNIBSS} {
		if findResource(&st, setup.KindSystem, id) == nil {
			t.Errorf("no %s system resource", id)
		}
	}
	if err := Init(cfg.Dir, banktest.Spec("beerus", today), DefaultLocal(), today); err == nil || !strings.Contains(err.Error(), "already holds") {
		t.Fatalf("second init: %v", err)
	}
}

func TestDatabaseURLFromEnvIsNeverWritten(t *testing.T) {
	const secret = "postgres://postgres:hunter2@db:5432/postgres"
	t.Setenv("BEERUS_DATABASE_URL", secret)
	cfg := initTestConfig(t, clock.Date{Y: 2026, M: 11, D: 3})
	if cfg.DatabaseURL != secret {
		t.Fatalf("the environment did not win: %q", cfg.DatabaseURL)
	}
	for _, f := range []string{LocalFile, SpecFile, statusFile} {
		raw, err := os.ReadFile(cfg.Path(f))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "hunter2") {
			t.Fatalf("%s holds the database password", f)
		}
	}
}

func TestInitRefusesABadSpec(t *testing.T) {
	today := clock.Date{Y: 2026, M: 11, D: 3}
	spec := banktest.Spec("beerus", today)
	spec.Spec.Banks[1].ID = "cbn"
	dir := t.TempDir()
	if err := Init(dir, spec, DefaultLocal(), today); err == nil || !strings.Contains(err.Error(), "reserved for the CBN") {
		t.Fatalf("init with a reserved id: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, statusFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused spec left a status store: %v", err)
	}
}

func TestStateFromAFutureClockIsRefused(t *testing.T) {
	cfg := initTestConfig(t, clock.Date{Y: 2026, M: 11, D: 3})
	future := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	if err := os.WriteFile(cfg.Path("stub-state.json"), []byte(`{"clock":{"anchor_business":"`+future+`","pace":0}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(cfg, Options{Logger: slog.New(slog.DiscardHandler)}); !errors.Is(err, ErrFutureWorld) {
		t.Fatalf("New with a state ahead of real time: %v", err)
	}
	past := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	if err := os.WriteFile(cfg.Path("stub-state.json"), []byte(`{"clock":{"anchor_business":"`+past+`","pace":0}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	app, err := New(cfg, Options{Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatalf("New with an old frozen clock: %v", err)
	}
	t.Cleanup(func() {
		if err := app.DropBankDatabases(); err != nil {
			t.Errorf("drop databases: %v", err)
		}
	})
	if err := app.Save(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(cfg.Path("stub-state.json"))
	if err != nil || strings.Contains(string(raw), `"clock"`) {
		t.Fatalf("saved state still holds a clock: %v %s", err, raw)
	}
}

func TestClockIsReal(t *testing.T) {
	real := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	app := newTestApp(t, Options{Real: func() time.Time { return real }})
	if got := app.Clock().Now(); !got.Equal(real) || got.Location() != clock.Lagos {
		t.Fatalf("business time %v, want %v in Lagos", got, real)
	}
	if app.setupKey == 0 || app.setupKey != app.ctl.setupKey() {
		t.Fatalf("setup key %d", app.setupKey)
	}
}
