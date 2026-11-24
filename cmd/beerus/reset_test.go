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
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
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
	err := cmdReset([]string{"beerus", "--dir", dir}, &out)
	if _, statErr := os.Stat(filepath.Join(dir, setup.SpecFile)); !errors.Is(err, install.ErrWrongName) || !strings.Contains(err.Error(), `"lagos"`) || statErr != nil {
		t.Fatalf("reset with the wrong name: %v, or it touched the dir", err)
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

func seeded(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := seed.Init(dir, banktest.Spec(defaultSetupName), setup.DefaultLocal(), resetToday); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestReset(t *testing.T) {
	url := os.Getenv("BEERUS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set BEERUS_TEST_DATABASE_URL to run")
	}
	t.Setenv(config.DatabaseURLEnv, url)
	rows := []struct {
		name  string
		setup func(*testing.T) (name, dir, want string)
	}{
		{"version one with start and pace", func(t *testing.T) (string, string, string) {
			return "old-v1", writeOldDir(t, map[string]string{"control.json": versionOneStatus, "setup.yaml": oldSetupYAML, "local.yaml": "pace: 60\n"}), "Cleared "
		}},
		{"an old directory layout", func(t *testing.T) (string, string, string) {
			dir := writeOldDir(t, map[string]string{"banks/bank-a/bank.json": "{}", "beerus.yaml": "seed: 7\ndatabase_url: postgres:///nowhere\n"})
			db := config.DatabaseName(dir, "bank-a")
			openNIBSS(t, url, db)
			return "old-v1", dir, db
		}},
		{"a snapshot database and one outside the prefix that the setup owns", func(t *testing.T) (string, string, string) {
			dir := seeded(t)
			live, custom := config.DatabaseName(dir, setup.SystemNIBSS), "beerus_zz_"+config.HashKey(dir)[:8]
			openNIBSS(t, url, live)
			openNIBSS(t, url, custom)
			st, err := setup.LoadStatus(dir)
			if err != nil {
				t.Fatal(err)
			}
			st.Resources[0].Owned = append(st.Resources[0].Owned, setup.Owned{Kind: "database", Name: custom})
			if err := setup.SaveStatus(dir, st); err != nil {
				t.Fatal(err)
			}
			leftover := live + "_snap_old"
			conn, err := pgx.Connect(t.Context(), url)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { conn.Close(context.Background()) })
			if _, err := conn.Exec(t.Context(), `CREATE DATABASE `+leftover+` TEMPLATE `+live); err != nil {
				t.Fatal(err)
			}
			return defaultSetupName, dir, "Dropped databases:\n  " + live + "\n  " + leftover + "\n  " + custom + "\nCleared "
		}},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			name, dir, want := r.setup(t)
			var out bytes.Buffer
			if err := cmdReset([]string{name, "--dir", dir}, &out); err != nil {
				t.Fatal(err)
			}
			prefix := strings.TrimSuffix(config.DatabaseName(dir, "x"), "x")
			left, err := postgres.List(t.Context(), url, prefix)
			if ok, initErr := setup.Initialized(dir); !strings.Contains(out.String(), want) || !strings.Contains(out.String(), "Cleared "+dir) || ok || initErr != nil || err != nil || len(left) != 0 {
				t.Fatalf("output %q lacks %q, or the reset left %v (%v, %v, %v)", out.String(), want, left, ok, initErr, err)
			}
		})
	}
}
