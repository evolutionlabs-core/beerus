package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/evolutionlabs-core/beerus/internal/bank/banktest"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/install"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/postgres"
	"github.com/evolutionlabs-core/beerus/internal/seed"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

const (
	versionOneStatus = `{"version":1,"setup":"old-v1","clock":{"anchor_business":"2026-11-04T08:00:00+01:00"}}`
	oldSetupYAML     = "apiVersion: beerus.dev/v1\nkind: Setup\nmetadata:\n  name: old-v1\nspec:\n  start: 2026-11-04T08:00:00+01:00\n"
)

var resetToday = clock.Date{Y: 2026, M: 11, D: 3}

func writeOldDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "old-v1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func openNIBSS(t *testing.T, url, name string) {
	t.Helper()
	s, err := nibss.Open(t.Context(), url, name)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}

func TestResetRefusals(t *testing.T) {
	dir := t.TempDir()
	if err := seed.Init(dir, banktest.Spec("lagos"), setup.DefaultLocal(), resetToday); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := cmdReset([]string{"beerus", "--dir", dir}, &out); !errors.Is(err, install.ErrWrongName) || !strings.Contains(err.Error(), `"lagos"`) {
		t.Fatalf("reset with the wrong name: %v", err)
	}
	if err := cmdReset([]string{"--dir", dir}, &out); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("reset without a name: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, setup.SpecFile)); err != nil {
		t.Fatalf("a refused reset touched the dir: %v", err)
	}
	old := writeOldDir(t, map[string]string{"control.json": versionOneStatus, "setup.yaml": oldSetupYAML, "local.yaml": "pace: 60\n"})
	want := "beerus: " + old + ": This setup was made by an older Beerus. Run beerus reset, then beerus init.\n"
	for _, args := range [][]string{{"up", "--dir", old}, {"get", "banks", "--dir", old}} {
		var o, e bytes.Buffer
		if code := report(&o, &e, dispatch(args)); code != 1 || e.String() != want || o.Len() != 0 {
			t.Errorf("beerus %v: exit %d, stdout %q, stderr %q", args, code, o.String(), e.String())
		}
	}
}

func TestReset(t *testing.T) {
	url := os.Getenv("BEERUS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set BEERUS_TEST_DATABASE_URL to run")
	}
	t.Setenv(config.DatabaseURLEnv, url)
	older := func(files map[string]string) func(*testing.T) (string, string, string) {
		return func(t *testing.T) (string, string, string) {
			return "old-v1", writeOldDir(t, files), "Cleared "
		}
	}
	rows := []struct {
		name  string
		setup func(*testing.T) (name, dir, want string)
	}{
		{"version one with start and pace", older(map[string]string{"control.json": versionOneStatus, "setup.yaml": oldSetupYAML, "local.yaml": "pace: 60\n"})},
		{"version zero", older(map[string]string{"control.json": `{"version":0,"setup":"old-v1"}`, "setup.yaml": oldSetupYAML})},
		{"corrupt control.json", older(map[string]string{"control.json": "{", "setup.yaml": oldSetupYAML})},
		{"empty control.json", older(map[string]string{"control.json": "", "setup.yaml": oldSetupYAML})},
		{"missing setup.yaml", older(map[string]string{"control.json": versionOneStatus})},
		{"an old directory layout", func(t *testing.T) (string, string, string) {
			dir := filepath.Join(t.TempDir(), "old-setup")
			if err := os.MkdirAll(filepath.Join(dir, "banks", "bank-a"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "beerus.yaml"), []byte("seed: 7\ndatabase_url: postgres:///nowhere\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := setup.Initialized(dir); !errors.Is(err, setup.ErrOldSetup) {
				t.Fatalf("old dir before reset: %v", err)
			}
			db := config.DatabaseName(dir, "bank-a")
			openNIBSS(t, url, db)
			return "old-setup", dir, db
		}},
		{"a database outside the prefix that the setup owns", func(t *testing.T) (string, string, string) {
			dir := t.TempDir()
			if err := seed.Init(dir, banktest.Spec(defaultSetupName), setup.DefaultLocal(), resetToday); err != nil {
				t.Fatal(err)
			}
			custom := "beerus_custom_" + config.HashKey(dir)[:8]
			openNIBSS(t, url, custom)
			st := mustStatus(t, dir)
			st.Resources[0].Owned = append(st.Resources[0].Owned, setup.Owned{Kind: "database", Name: custom})
			if err := setup.SaveStatus(dir, st); err != nil {
				t.Fatal(err)
			}
			return defaultSetupName, dir, custom
		}},
		{"a leftover NIBSS snapshot database", func(t *testing.T) (string, string, string) {
			dir := t.TempDir()
			if err := seed.Init(dir, banktest.Spec(defaultSetupName), setup.DefaultLocal(), resetToday); err != nil {
				t.Fatal(err)
			}
			live := config.DatabaseName(dir, setup.SystemNIBSS)
			openNIBSS(t, url, live)
			leftover := live + "_snap_old"
			conn, err := pgx.Connect(t.Context(), url)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { conn.Close(context.Background()) })
			if _, err := conn.Exec(t.Context(), `CREATE DATABASE `+leftover+` TEMPLATE `+live); err != nil {
				t.Fatal(err)
			}
			return defaultSetupName, dir, "Dropped databases:\n  " + live + "\n  " + leftover + "\nCleared "
		}},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			name, dir, want := r.setup(t)
			var out bytes.Buffer
			if err := cmdReset([]string{name, "--dir", dir}, &out); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), want) || !strings.Contains(out.String(), "Cleared "+dir) {
				t.Fatalf("output %q lacks %q", out.String(), want)
			}
			if ok, err := setup.Initialized(dir); err != nil || ok {
				t.Fatalf("after reset: %v %v", ok, err)
			}
			prefix := strings.TrimSuffix(config.DatabaseName(dir, "x"), "x")
			if left, err := postgres.List(t.Context(), url, prefix); err != nil || len(left) != 0 {
				t.Fatalf("databases left after reset: %v %v", left, err)
			}
		})
	}
}
