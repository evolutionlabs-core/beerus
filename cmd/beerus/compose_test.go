package main

import (
	"github.com/evolutionlabs-core/beerus/internal/seed"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func newSetup(t *testing.T, local setup.Local) *config.Config {
	t.Helper()
	dir := t.TempDir()
	today := clock.DateOf(time.Now())
	if err := seed.Init(dir, setup.DefaultSpec(defaultSetupName), local, today); err != nil {
		t.Fatal(err)
	}
	c, err := setup.LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestComposeFollowsTheSetup(t *testing.T) {
	c := newSetup(t, setup.DefaultLocal())
	out, err := renderCompose(c)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := os.ReadFile("../../compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(committed) {
		t.Fatal("compose.yaml is not what `beerus compose` writes for a new setup; regenerate it")
	}
	c.Banks = append(c.Banks, &config.Bank{ID: "bank-a", Name: "Bank A", Type: "commercial", Port: 7445, APIPort: 7455, State: "closed"}, &config.Bank{ID: "bank-c", Name: "Bank C", Type: "commercial", Port: 7460, APIPort: 7470})
	c.Connect.Host, c.Connect.HealthPort = "connect.example.localhost", 7555
	out, err = renderCompose(c)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, want := range []string{"  cbn:", "cbn.localhost", "  nibss:", "network_mode: \"service:core\"", "  connect:", "  bank-c:", "127.0.0.1:7460:7460", "bank-c.localhost", "connect.example.localhost", "BEERUS_BIND: 0.0.0.0", "BEERUS_HEALTH_PORT: \"7555\""} {
		if !strings.Contains(text, want) {
			t.Errorf("compose file lacks %q", want)
		}
	}
	if strings.Contains(text, "  bank-a:") {
		t.Error("compose file holds the closed bank-a")
	}
	c.Connect.HealthPort = 0
	if _, err := renderCompose(c); err == nil || !strings.Contains(err.Error(), "health_port") {
		t.Errorf("a setup without a health port rendered: %v", err)
	}
}
