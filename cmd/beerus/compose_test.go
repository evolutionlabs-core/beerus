package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

func TestComposeFollowsTheSetup(t *testing.T) {
	c, err := config.Init(t.TempDir(), config.InitOptions{Today: clock.DateOf(time.Now())})
	if err != nil {
		t.Fatal(err)
	}
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
	c.Banks = append(c.Banks, &config.Bank{ID: "bank-c", Name: "Bank C", Type: "commercial", Port: 7460, APIPort: 7470})
	c.Banks[0].State = "closed"
	c.Connect.Host = "connect.example.localhost"
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddPartner(config.PartnerOptions{ID: "ledger", Name: "Ledger", LegalName: "Ledger Ltd", Purpose: "to reconcile", RedirectURIs: []string{"https://ledger.localhost/cb"}}); err != nil {
		t.Fatal(err)
	}
	out, err = renderCompose(c)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, want := range []string{"  bank-b:", "  bank-c:", "127.0.0.1:7460:7460", "bank-c.localhost", "connect.example.localhost", "BEERUS_BANK_PORT: \"7446\"", "BEERUS_BIND: 0.0.0.0"} {
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
	c, err := config.Init(t.TempDir(), config.InitOptions{Today: clock.DateOf(time.Now()), HealthPort: 7555})
	if err != nil {
		t.Fatal(err)
	}
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
