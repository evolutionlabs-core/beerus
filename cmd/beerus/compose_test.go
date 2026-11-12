package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/connect"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func newSetup(t *testing.T, local setup.Local) *config.Config {
	t.Helper()
	dir := t.TempDir()
	today := clock.DateOf(time.Now())
	if err := connect.Init(dir, setup.DefaultSpec(defaultSetupName, today), local, today); err != nil {
		t.Fatal(err)
	}
	c, err := connect.LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestComposeFollowsTheSetup(t *testing.T) {
	c := newSetup(t, connect.DefaultLocal())
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
	c.Banks = append(c.Banks,
		&config.Bank{ID: "bank-a", Name: "Bank A", Type: "commercial", Port: 7445, APIPort: 7455, State: "closed"},
		&config.Bank{ID: "bank-b", Name: "Bank B", Type: "digital", Port: 7446, APIPort: 7456},
		&config.Bank{ID: "bank-c", Name: "Bank C", Type: "commercial", Port: 7460, APIPort: 7470},
	)
	c.Connect.Host = "connect.example.localhost"
	c.Partners = append(c.Partners, &config.Partner{ID: "ledger", Name: "Ledger", LegalName: "Ledger Ltd", Purpose: "to reconcile", RedirectURIs: []string{"https://ledger.localhost/cb"}})
	out, err = renderCompose(c)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, want := range []string{"  bank-b:", "  bank-c:", "127.0.0.1:7460:7460", "bank-c.localhost", "connect.example.localhost", "BEERUS_BIND: 0.0.0.0"} {
		if !strings.Contains(text, want) {
			t.Errorf("compose file lacks %q", want)
		}
	}
	for _, unwanted := range []string{"  bank-a:", "BEERUS_PARTNER_ID", "balance", "Evolution Labs"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("compose file holds %q", unwanted)
		}
	}
}

func TestComposeFollowsTheHealthPort(t *testing.T) {
	local := connect.DefaultLocal()
	local.Connect.HealthPort = 7555
	c := newSetup(t, local)
	out, err := renderCompose(c)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, want := range []string{"127.0.0.1:7555:7555", "http://127.0.0.1:7555/health", "BEERUS_HEALTH_PORT: \"7555\""} {
		if !strings.Contains(text, want) {
			t.Errorf("compose file lacks %q", want)
		}
	}
	if strings.Contains(text, "7490") {
		t.Error("compose file still holds the default health port")
	}
	c.Connect.HealthPort = 0
	if _, err := renderCompose(c); err == nil || !strings.Contains(err.Error(), "health_port") {
		t.Errorf("a setup without a health port rendered: %v", err)
	}
}
