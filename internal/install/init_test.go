package install

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/bank/banktest"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/state"
)

func initConfig(t *testing.T, today clock.Date, local setup.Local) *config.Config {
	t.Helper()
	dir := t.TempDir()
	if u := os.Getenv("BEERUS_TEST_DATABASE_URL"); u != "" {
		local.DatabaseURL = u
	}
	if err := Init(dir, banktest.Spec("beerus"), local, today); err != nil {
		t.Fatal(err)
	}
	cfg, err := state.LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestOldDirIsRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, state.OldSpecFile), []byte("seed: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"load":  func() error { _, err := state.LoadConfig(dir); return err }(),
		"init":  Init(dir, banktest.Spec("beerus"), DefaultLocal(), clock.Date{Y: 2026, M: 11, D: 3}),
		"check": func() error { _, err := state.Initialized(dir); return err }(),
	} {
		if !errors.Is(err, setup.ErrOldSetup) || !strings.Contains(err.Error(), "This setup was made by an older Beerus. Run beerus reset, then beerus init.") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, state.StatusFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an old dir got a status store: %v", err)
	}
}

func TestInitWritesTheDefaultSetup(t *testing.T) {
	today := clock.Date{Y: 2026, M: 11, D: 3}
	cfg := initConfig(t, today, DefaultLocal())
	for _, f := range []string{state.SpecFile, state.LocalFile, state.StatusFile, "control-token"} {
		fi, err := os.Stat(cfg.Path(f))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s has mode %v", f, fi.Mode().Perm())
		}
	}
	raw, err := os.ReadFile(cfg.Path(state.SpecFile))
	if err != nil {
		t.Fatal(err)
	}
	spec, errs := setup.Parse(state.SpecFile, raw)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if len(spec.Spec.Banks) != 2 {
		t.Fatalf("setup.yaml: %+v", spec.Spec)
	}
	want := []struct {
		id, typ, nip, nuban string
		port, api           int
	}{{"bank-a", "commercial", "000001", "001", 7445, 7455}, {"bank-b", "digital", "000002", "002", 7448, 7458}}
	for i, w := range want {
		b := cfg.Banks[i]
		if b.ID != w.id || b.Type != w.typ || b.NIPCode != w.nip || b.NUBANCode != w.nuban || b.Port != w.port || b.APIPort != w.api || b.Database == "" {
			t.Errorf("bank %d: %+v", i, b)
		}
		if b.HistoryDays != state.TypeHistoryDays[w.typ] {
			t.Errorf("%s history %d", b.ID, b.HistoryDays)
		}
	}
	st, err := setup.LoadStatus(cfg.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.SetupKey == 0 || st.Setup != "beerus" || st.AppliedSHA256 == "" {
		t.Fatalf("status: key %d setup %q sum %q", st.SetupKey, st.Setup, st.AppliedSHA256)
	}
	if st.Applied.Spec.Banks[0].NIPCode != "000001" {
		t.Errorf("applied codes are not pinned: %+v", st.Applied.Spec.Banks[0])
	}
	r := state.FindResource(&st, setup.KindBank, "bank-b")
	if r == nil || r.Phase != config.PhaseProvisioning || r.Steps[config.StepAllocate] != state.StepDone || r.Generation != 1 {
		t.Fatalf("bank-b resource: %+v", r)
	}
	for _, id := range []string{setup.SystemCBN, setup.SystemNIBSS} {
		if state.FindResource(&st, setup.KindSystem, id) == nil {
			t.Errorf("no %s system resource", id)
		}
	}
	if err := Init(cfg.Dir, banktest.Spec("beerus"), DefaultLocal(), today); err == nil || !strings.Contains(err.Error(), "already holds") {
		t.Fatalf("second init: %v", err)
	}
}

func TestDatabaseURLFromEnvIsNeverWritten(t *testing.T) {
	const secret = "postgres://postgres:hunter2@db:5432/postgres"
	t.Setenv("BEERUS_DATABASE_URL", secret)
	cfg := initConfig(t, clock.Date{Y: 2026, M: 11, D: 3}, DefaultLocal())
	if cfg.DatabaseURL != secret {
		t.Fatalf("the environment did not win: %q", cfg.DatabaseURL)
	}
	for _, f := range []string{state.LocalFile, state.SpecFile, state.StatusFile} {
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
	spec := banktest.Spec("beerus")
	spec.Spec.Banks[1].ID = "cbn"
	dir := t.TempDir()
	if err := Init(dir, spec, DefaultLocal(), today); err == nil || !strings.Contains(err.Error(), "reserved for the CBN") {
		t.Fatalf("init with a reserved id: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, state.StatusFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused spec left a status store: %v", err)
	}
}

func TestNewSetupKeyIsRandom(t *testing.T) {
	a, err := newSetupKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err := newSetupKey()
	if err != nil {
		t.Fatal(err)
	}
	if a == 0 || a == b {
		t.Fatalf("keys %d and %d", a, b)
	}
}
