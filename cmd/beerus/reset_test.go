package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/bank/banktest"
	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/connect"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/jackc/pgx/v5"
)

var resetToday = clock.Date{Y: 2026, M: 11, D: 3}

func TestResetRefusesTheWrongName(t *testing.T) {
	dir := t.TempDir()
	if err := connect.Init(dir, banktest.Spec("lagos", resetToday), connect.DefaultLocal(), resetToday); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := cmdReset([]string{"beerus", "--dir", dir}, &out); !errors.Is(err, connect.ErrWrongName) || !strings.Contains(err.Error(), `"lagos"`) {
		t.Fatalf("reset with the wrong name: %v", err)
	}
	if err := cmdReset([]string{"--dir", dir}, &out); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("reset without a name: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, connect.SpecFile)); err != nil {
		t.Fatalf("a refused reset touched the dir: %v", err)
	}
}

func TestResetClearsAnOldDir(t *testing.T) {
	url := os.Getenv("BEERUS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set BEERUS_TEST_DATABASE_URL to run")
	}
	t.Setenv(config.DatabaseURLEnv, url)
	dir := filepath.Join(t.TempDir(), "old-setup")
	if err := os.MkdirAll(filepath.Join(dir, "banks", "bank-a"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "beerus.yaml"), []byte("seed: 7\ndatabase_url: postgres:///nowhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	db := config.DatabaseName(dir, "bank-a")
	s, err := nibss.Open(context.Background(), url, db)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := connect.Initialized(dir); !errors.Is(err, connect.ErrOldSetup) {
		t.Fatalf("old dir before reset: %v", err)
	}
	var out bytes.Buffer
	if err := cmdReset([]string{"old-setup", "--dir", dir}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), db) || !strings.Contains(out.String(), "Cleared "+dir) {
		t.Fatalf("output: %s", out.String())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("left in the dir: %v", entries)
	}
	ok, err := connect.Initialized(dir)
	if err != nil || ok {
		t.Fatalf("after reset: %v %v", ok, err)
	}
	if err := connect.Init(dir, banktest.Spec(defaultSetupName, resetToday), connect.DefaultLocal(), resetToday); err != nil {
		t.Fatalf("up after reset: %v", err)
	}
	if _, err := connect.LoadConfig(dir); err != nil {
		t.Fatalf("load after reset: %v", err)
	}
}

func TestResetDropsOwnedDatabasesOutsideThePrefix(t *testing.T) {
	url := os.Getenv("BEERUS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set BEERUS_TEST_DATABASE_URL to run")
	}
	t.Setenv(config.DatabaseURLEnv, url)
	dir := t.TempDir()
	if err := connect.Init(dir, banktest.Spec(defaultSetupName, resetToday), connect.DefaultLocal(), resetToday); err != nil {
		t.Fatal(err)
	}
	custom := "beerus_custom_" + config.HashKey(dir)[:8]
	s, err := nibss.Open(context.Background(), url, custom)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	st, err := setup.LoadStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Resources[0].Owned = append(st.Resources[0].Owned, setup.Owned{Kind: "database", Name: custom})
	if err := setup.SaveStatus(dir, st); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := cmdReset([]string{defaultSetupName, "--dir", dir}, &out); err != nil {
		t.Fatal(err)
	}
	left, err := store.List(context.Background(), url, custom)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 || !strings.Contains(out.String(), custom) {
		t.Fatalf("custom database left: %v; output: %s", left, out.String())
	}
}

func TestResetDropsALeftoverNIBSSSnapshotDatabase(t *testing.T) {
	url := os.Getenv("BEERUS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set BEERUS_TEST_DATABASE_URL to run")
	}
	t.Setenv(config.DatabaseURLEnv, url)
	ctx := context.Background()
	dir := t.TempDir()
	if err := connect.Init(dir, banktest.Spec(defaultSetupName, resetToday), connect.DefaultLocal(), resetToday); err != nil {
		t.Fatal(err)
	}
	live := config.DatabaseName(dir, setup.SystemNIBSS)
	s, err := nibss.Open(ctx, url, live)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	leftover := live + "_snap_old"
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(ctx) })
	if _, err := conn.Exec(ctx, `CREATE DATABASE `+leftover+` TEMPLATE `+live); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.DropDatabases(ctx, url, []string{leftover, live}); err != nil {
			t.Error(err)
		}
	})
	var out bytes.Buffer
	if err := cmdReset([]string{defaultSetupName, "--dir", dir}, &out); err != nil {
		t.Fatal(err)
	}
	want := "Dropped databases:\n  " + live + "\n  " + leftover + "\nCleared " + dir + " (kept " + connect.LocalFile + "). Run beerus init to start again.\n"
	if out.String() != want {
		t.Fatalf("output: %q, want %q", out.String(), want)
	}
	left, err := store.List(ctx, url, strings.TrimSuffix(config.DatabaseName(dir, "x"), "x"))
	if err != nil || len(left) != 0 {
		t.Fatalf("databases left after reset: %v %v", left, err)
	}
}
