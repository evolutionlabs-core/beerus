package core

import (
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/banktest"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/seed"
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
	local := setup.DefaultLocal()
	ports := testPorts(t, 2)
	local.Connect.PartnerPort, local.Connect.WidgetPort, local.Connect.ControlPort, local.Connect.HealthPort, local.Connect.NIBSSPort, local.Connect.CBNPort, local.Connect.ConnectControlPort = ports[0], ports[1], ports[2], ports[3], ports[4], ports[5], ports[7]
	return initConfigWith(t, today, local)
}

func initConfigWith(t *testing.T, today clock.Date, local setup.Local) *config.Config {
	t.Helper()
	dir := t.TempDir()
	if u := os.Getenv("BEERUS_TEST_DATABASE_URL"); u != "" {
		local.DatabaseURL = u
	}
	if err := seed.Init(dir, banktest.Spec("beerus"), local, today); err != nil {
		t.Fatal(err)
	}
	cfg, err := setup.LoadConfig(dir)
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
	need := 8 + banks
	for range 200 {
		base := testPortLow + rand.IntN(testPortHigh-testPortLow-need-config.DefaultAPIPortOffset)
		free := true
		for p := base; p < base+need && free; p++ {
			free = config.PortFree(p) && (p < base+8 || config.PortFree(p+config.DefaultAPIPortOffset))
		}
		if free {
			return []int{base, base + 1, base + 2, base + 3, base + 4, base + 5, base + 6, base + 7}
		}
	}
	t.Fatal("no free block of ports for the app")
	return nil
}

func TestWriteFilesRestoresExactlyTheSavedSet(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "partner")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"api.key": "old", "other.key": "old", "stale.txt": "old"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeFiles(dir, map[string][]byte{"api.key": []byte("new"), "extra.txt": []byte("text")}); err != nil {
		t.Fatal(err)
	}
	got, err := readFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || string(got["api.key"]) != "new" || string(got["extra.txt"]) != "text" {
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
