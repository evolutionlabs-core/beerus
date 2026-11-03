package world

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

var originalPersonas = []string{"adaeze", "tunde", "ngozi", "chidi"}

var originalDigests = map[int64]string{
	42:       "94ad53226681578a2b42f704095a46dc84333211d1a8bf1f61453a5653cd7135",
	20261028: "b5f355ae8f54f1e61a4818c43ba1f64f8d88afc6b0f720144598292d00d5c70b",
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
	if len(ps) < 18 || len(ps) > 22 {
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
