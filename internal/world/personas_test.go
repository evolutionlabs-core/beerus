package world

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

var originalPersonas = []string{"adaeze", "tunde", "ngozi", "chidi"}

var originalDigests = map[int64]string{
	42:       "c234e0a92104aeb077c6bb5070b97ef24bc6adf66bb6ef8a86eaa9d97dace9a5",
	20261028: "6961fdb8266fa51722bed302d50e52cc192d27841457cd703753bb6cc695e6b7",
}

func originalsDigest(seed int64) (string, int) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, clock.Lagos)
	w := New(Config{Seed: seed, Genesis: clock.DateOf(now).AddDays(-760)}, Overrides{})
	h := sha256.New()
	lines := 0
	enc := json.NewEncoder(h)
	for _, id := range originalPersonas {
		p, _ := w.Persona(id)
		enc.Encode(p)
		for _, k := range p.Accounts {
			a, _ := w.Account(k)
			enc.Encode(a)
			ls := w.LinesFor(k, now)
			lines += len(ls)
			for _, l := range ls {
				enc.Encode(l)
				enc.Encode(l.BalanceAfter)
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil)), lines
}

func TestOriginalPersonasUnchanged(t *testing.T) {
	for seed, want := range originalDigests {
		got, lines := originalsDigest(seed)
		if lines < 1000 {
			t.Fatalf("seed %d: only %d lines", seed, lines)
		}
		if got != want {
			t.Errorf("seed %d: the original personas' accounts or history changed (%s)", seed, got)
		}
	}
}

func TestPersonaCount(t *testing.T) {
	w, _ := testWorld()
	ps := w.Personas()
	if len(ps) != 20 {
		t.Fatalf("%d personas", len(ps))
	}
	ids, names, users, nubans := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, p := range ps {
		if ids[p.ID] || names[p.Name] || users[p.Username] {
			t.Fatalf("duplicate persona %s", p.ID)
		}
		ids[p.ID], names[p.Name], users[p.Username] = true, true, true
		if len(p.Accounts) == 0 {
			t.Fatalf("%s has no accounts", p.ID)
		}
		for _, k := range p.Accounts {
			a, _ := w.Account(k)
			if nubans[a.NUBAN] {
				t.Fatalf("duplicate NUBAN %s", a.NUBAN)
			}
			nubans[a.NUBAN] = true
		}
	}
}

func TestPersonaFilterKeepsAccounts(t *testing.T) {
	full, _ := testWorld()
	cfg := full.Config()
	cfg.Personas = originalPersonas
	small := New(cfg, Overrides{})
	if got := len(small.Personas()); got != len(originalPersonas) {
		t.Fatalf("%d personas", got)
	}
	for _, id := range originalPersonas {
		want, _ := full.Persona(id)
		got, ok := small.Persona(id)
		if !ok || !slices.Equal(got.Accounts, want.Accounts) {
			t.Fatalf("%s accounts changed: %v vs %v", id, got.Accounts, want.Accounts)
		}
	}
}

func BenchmarkWorldGeneration(b *testing.B) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, clock.Lagos)
	for i := 0; i < b.N; i++ {
		w := New(Config{Seed: 42, Genesis: clock.DateOf(now).AddDays(-760)}, Overrides{})
		for _, p := range w.Personas() {
			for _, k := range p.Accounts {
				w.LinesFor(k, now)
			}
		}
	}
}
