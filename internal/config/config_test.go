package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

func load(t *testing.T, extra string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	body := "seed: 7\ngenesis: 2024-01-01\n" + extra
	if err := os.WriteFile(filepath.Join(dir, "beerus.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(dir)
}

func TestNIPSessionsAreValidated(t *testing.T) {
	c, err := load(t, "")
	if err != nil || c.Sessions() != DefaultNIPSessions {
		t.Fatalf("default: %v %v", c, err)
	}
	for _, ok := range []string{"1", "2", "12", "24", "1440"} {
		if _, err := load(t, "nip_sessions: "+ok+"\n"); err != nil {
			t.Errorf("nip_sessions %s: %v", ok, err)
		}
	}
	for _, bad := range []string{"-1", "7", "11", "2880", "1000000000"} {
		_, err := load(t, "nip_sessions: "+bad+"\n")
		if err == nil || !strings.Contains(err.Error(), "nip_sessions") {
			t.Errorf("nip_sessions %s: %v", bad, err)
		}
	}
}

func TestPortAllocation(t *testing.T) {
	c, err := load(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.APIPortOffset() != DefaultAPIPortOffset || c.PortWindow() != DefaultPortWindow {
		t.Fatalf("defaults %d %d", c.APIPortOffset(), c.PortWindow())
	}
	used := map[int]bool{8010: true, 8011: true}
	if p, err := c.AllocatePort(8010, used, func(p int) bool { return p != 8012 }); err != nil || p != 8013 {
		t.Fatalf("port %d: %v", p, err)
	}
	if _, err := c.AllocatePort(8010, used, func(int) bool { return false }); err == nil || !strings.Contains(err.Error(), "8010 to 8209") {
		t.Fatalf("no port free: %v", err)
	}
	c, err = load(t, "ports:\n  api_offset: 20\n  window: 3\n")
	if err != nil || c.APIPortOffset() != 20 {
		t.Fatalf("custom: %v", err)
	}
	if _, err := c.AllocatePort(9000, map[int]bool{9000: true, 9001: true, 9002: true}, func(int) bool { return true }); err == nil {
		t.Fatal("the window should stop the search")
	}
	if _, err := load(t, "ports:\n  window: -1\n"); err == nil {
		t.Fatal("negative window accepted")
	}
	if _, err := load(t, "banks:\n  - id: bank-a\n    open_banking:\n      dormant_days: -5\n"); err == nil || !strings.Contains(err.Error(), "dormant_days") {
		t.Fatalf("negative dormant_days: %v", err)
	}
}

func TestDatabaseURLFromEnvIsNeverSaved(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, InitOptions{Seed: 7, Today: clock.Date{Y: 2026, M: 11, D: 3}}); err != nil {
		t.Fatal(err)
	}
	const secret = "postgres://postgres:hunter2@db:5432/postgres"
	t.Setenv(DatabaseURLEnv, secret)
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.DatabaseURL != secret {
		t.Fatalf("the environment did not win: %q", c.DatabaseURL)
	}
	if _, err := c.AddPartner(PartnerOptions{ID: "p1", RedirectURIs: []string{"https://p1.localhost/cb"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(c.Path("beerus.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hunter2") {
		t.Fatalf("beerus.yaml holds the database password:\n%s", raw)
	}
	t.Setenv(DatabaseURLEnv, "")
	again, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again.DatabaseURL == secret {
		t.Fatal("the password came back from beerus.yaml")
	}
}
