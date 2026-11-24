package state

import (
	"slices"
	"sync"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Create(t.TempDir(), setup.Status{Version: setup.StatusVersion, Setup: "beerus", SetupKey: 9})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func owned(t *testing.T, s *Store, kind, id string) []setup.Owned {
	t.Helper()
	st, err := setup.LoadStatus(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	if r := setup.FindResource(&st, kind, id); r != nil {
		return r.Owned
	}
	return nil
}

func port(name string) setup.Owned {
	return setup.Owned{Kind: OwnedPort, Name: name}
}

func TestStoreClaims(t *testing.T) {
	s := testStore(t)
	db := setup.Owned{Kind: OwnedDatabase, Name: "beerus_x_bank_a"}
	for range 2 {
		if err := s.Claim(setup.KindBank, "bank-a", db, port("7445")); err != nil {
			t.Fatal(err)
		}
	}
	if s.version != 2 || !slices.Equal(owned(t, s, setup.KindBank, "bank-a"), []setup.Owned{db, port("7445")}) {
		t.Fatalf("a repeated claim wrote again: version %d", s.version)
	}
	if err := s.Release(port("7445")); err != nil || !slices.Equal(owned(t, s, setup.KindBank, "bank-a"), []setup.Owned{db}) {
		t.Fatalf("after release: %v", err)
	}
	if err := s.Forget(setup.KindBank, "bank-a"); err != nil || owned(t, s, setup.KindBank, "bank-a") != nil {
		t.Fatalf("after forget: %v", err)
	}
}

func TestStoreSkipsOlderCopies(t *testing.T) {
	s := testStore(t)
	add := func(name string) uint64 {
		v, err := s.change(func(st *setup.Status) (bool, error) {
			r := EnsureResource(st, setup.KindBank, "bank-a")
			r.Owned = append(r.Owned, port(name))
			return true, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	older, newer := add("1"), add("2")
	for _, v := range []uint64{newer, older} {
		if err := s.flush(v); err != nil {
			t.Fatal(err)
		}
	}
	if got := owned(t, s, setup.KindBank, "bank-a"); len(got) != 2 {
		t.Fatalf("an older copy overwrote a newer one: %+v", got)
	}
}

func TestStoreConcurrentClaims(t *testing.T) {
	s := testStore(t)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			if err := s.Claim(setup.KindBank, "bank-a", port(string(rune('a'+i)))); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got := owned(t, s, setup.KindBank, "bank-a"); len(got) != 20 {
		t.Fatalf("%d claims saved, want 20", len(got))
	}
}

func TestCBNPortClaimsFollowTheConfig(t *testing.T) {
	s := testStore(t)
	cfg := &config.Config{}
	for _, p := range []int{39100, 39200} {
		cfg.Connect.CBNPort = p
		if err := EnsureCBNPorts(s, cfg); err != nil {
			t.Fatal(err)
		}
	}
	got := owned(t, s, setup.KindSystem, setup.SystemCBN)
	if len(got) != 2 || !slices.Contains(got, port("39200")) || slices.Contains(got, port("39100")) {
		t.Fatalf("claims after the port changed: %+v", got)
	}
}
