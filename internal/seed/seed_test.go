package seed

import (
	"errors"
	"fmt"
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

var today = clock.Date{Y: 2026, M: 11, D: 3}

func initConfig(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	local := setup.DefaultLocal()
	if u := os.Getenv("BEERUS_TEST_DATABASE_URL"); u != "" {
		local.DatabaseURL = u
	}
	if err := Init(dir, banktest.Spec("beerus"), local, today); err != nil {
		t.Fatal(err)
	}
	cfg, err := setup.LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestInitRefusals(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, setup.OldSpecFile), []byte("seed: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Init(dir, banktest.Spec("beerus"), setup.DefaultLocal(), today)
	if !errors.Is(err, setup.ErrOldSetup) || !strings.Contains(err.Error(), "This setup was made by an older Beerus. Run beerus reset, then beerus init.") {
		t.Errorf("old setup: %v", err)
	}
	bad := banktest.Spec("beerus")
	bad.Spec.Banks[1].ID = "cbn"
	fresh := t.TempDir()
	if err := Init(fresh, bad, setup.DefaultLocal(), today); err == nil || !strings.Contains(err.Error(), "reserved for the CBN") {
		t.Errorf("reserved id: %v", err)
	}
	for _, d := range []string{dir, fresh} {
		if _, err := os.Stat(filepath.Join(d, setup.StatusFile)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("a refused init left a status store in %s: %v", d, err)
		}
	}
}

func TestInitWritesTheDefaultSetup(t *testing.T) {
	cfg := initConfig(t)
	for _, f := range []string{setup.SpecFile, setup.LocalFile, setup.StatusFile, "control-token"} {
		if fi, err := os.Stat(cfg.Path(f)); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v, %v", f, fi, err)
		}
	}
	for i, want := range []string{"bank-a commercial 000001 001 7445 7455 730", "bank-b digital 000002 002 7448 7458 180"} {
		b := cfg.Banks[i]
		if got := fmt.Sprint(b.ID, " ", b.Type, " ", b.NIPCode, " ", b.NUBANCode, " ", b.Port, " ", b.APIPort, " ", b.HistoryDays); got != want || b.Database == "" {
			t.Errorf("bank %d: %q, want %q, database %q", i, got, want, b.Database)
		}
	}
	st, err := setup.LoadStatus(cfg.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.SetupKey == 0 || st.Setup != "beerus" || st.AppliedSHA256 == "" || st.Applied.Spec.Banks[0].NIPCode != "000001" {
		t.Fatalf("status: key %d setup %q sum %q", st.SetupKey, st.Setup, st.AppliedSHA256)
	}
	if r := setup.FindResource(&st, setup.KindBank, "bank-b"); r == nil || r.Phase != config.PhaseProvisioning || r.Steps[config.StepAllocate] != state.StepDone || r.Generation != 1 {
		t.Fatalf("bank-b resource: %+v", r)
	}
	if err := Init(cfg.Dir, banktest.Spec("beerus"), setup.DefaultLocal(), today); err == nil || !strings.Contains(err.Error(), "already holds") {
		t.Fatalf("second init: %v", err)
	}
}

func TestDatabaseURLFromEnvIsNeverWritten(t *testing.T) {
	const secret = "postgres://postgres:hunter2@db:5432/postgres"
	t.Setenv("BEERUS_DATABASE_URL", secret)
	cfg := initConfig(t)
	if cfg.DatabaseURL != secret {
		t.Fatalf("the environment did not win: %q", cfg.DatabaseURL)
	}
	for _, f := range []string{setup.LocalFile, setup.SpecFile, setup.StatusFile} {
		if raw, err := os.ReadFile(cfg.Path(f)); err != nil || strings.Contains(string(raw), "hunter2") {
			t.Fatalf("%s: %v", f, err)
		}
	}
}
