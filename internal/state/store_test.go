package state

import (
	"os"
	"path/filepath"
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

func diskStatus(t *testing.T, s *Store) setup.Status {
	t.Helper()
	st, err := setup.LoadStatus(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestStoreClaimIsIdempotent(t *testing.T) {
	s := testStore(t)
	db := setup.Owned{Kind: OwnedDatabase, Name: "beerus_x_bank_a"}
	port := setup.Owned{Kind: OwnedPort, Name: "7445"}
	for range 2 {
		if err := s.Claim(setup.KindBank, "bank-a", db, port); err != nil {
			t.Fatal(err)
		}
	}
	if s.version != 2 {
		t.Fatalf("a repeated claim wrote the store again: version %d", s.version)
	}
	got := diskStatus(t, s).Resources
	if len(got) != 1 || !slices.Equal(got[0].Owned, []setup.Owned{db, port}) {
		t.Fatalf("resources: %+v", got)
	}
	if err := s.Release(port); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(setup.Owned{Kind: OwnedPort, Name: "9999"}); err != nil {
		t.Fatalf("releasing an unknown name: %v", err)
	}
	if got := diskStatus(t, s).Resources[0].Owned; !slices.Equal(got, []setup.Owned{db}) {
		t.Fatalf("after release: %+v", got)
	}
	if err := s.Forget(setup.KindBank, "bank-a"); err != nil {
		t.Fatal(err)
	}
	if got := diskStatus(t, s).Resources; len(got) != 0 {
		t.Fatalf("after forget: %+v", got)
	}
}

func TestStoreSkipsOlderCopies(t *testing.T) {
	s := testStore(t)
	add := func(name string) uint64 {
		v, err := s.change(func(st *setup.Status) (bool, error) {
			r := EnsureResource(st, setup.KindBank, "bank-a")
			r.Owned = append(r.Owned, setup.Owned{Kind: OwnedPort, Name: name})
			return true, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	ov := add("1")
	nv := add("2")
	if err := s.flush(nv); err != nil {
		t.Fatal(err)
	}
	if err := s.flush(ov); err != nil {
		t.Fatal(err)
	}
	if got := diskStatus(t, s).Resources[0].Owned; len(got) != 2 {
		t.Fatalf("an older copy overwrote a newer one: %+v", got)
	}
}

func TestStoreFlushWritesLatestAfterFailedWrite(t *testing.T) {
	s := testStore(t)
	add := func(name string) uint64 {
		v, err := s.change(func(st *setup.Status) (bool, error) {
			r := EnsureResource(st, setup.KindBank, "bank-a")
			r.Owned = append(r.Owned, setup.Owned{Kind: OwnedPort, Name: name})
			return true, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	ov := add("1")
	add("2")
	if err := s.flush(ov); err != nil {
		t.Fatal(err)
	}
	if got := diskStatus(t, s).Resources[0].Owned; len(got) != 2 {
		t.Fatalf("an older flush wrote an older state: %+v", got)
	}
}

func TestStoreConcurrentClaims(t *testing.T) {
	s := testStore(t)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Claim(setup.KindBank, "bank-a", setup.Owned{Kind: OwnedPort, Name: string(rune('a' + i))}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := diskStatus(t, s).Resources[0].Owned; len(got) != 20 {
		t.Fatalf("%d claims saved, want 20", len(got))
	}
}

func TestStoreFileMode(t *testing.T) {
	s := testStore(t)
	fi, err := os.Stat(filepath.Join(s.dir, "control.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
}

func TestCBNPortClaimsFollowTheConfig(t *testing.T) {
	s := testStore(t)
	cfg := &config.Config{}
	cfg.Connect.CBNPort = 39100
	for _, port := range []int{39100, 39200} {
		cfg.Connect.CBNPort = port
		if err := EnsureCBNPorts(s, cfg); err != nil {
			t.Fatal(err)
		}
	}
	r := FindResource(&setup.Status{Resources: diskStatus(t, s).Resources}, setup.KindSystem, setup.SystemCBN)
	if r == nil || len(r.Owned) != 2 || !slices.Contains(r.Owned, setup.Owned{Kind: OwnedPort, Name: "39200"}) || slices.Contains(r.Owned, setup.Owned{Kind: OwnedPort, Name: "39100"}) {
		t.Fatalf("claims after the port changed: %+v", r)
	}
	if err := s.ReleasePorts(setup.KindSystem, setup.SystemCBN); err != nil {
		t.Fatal(err)
	}
	if r := FindResource(&setup.Status{Resources: diskStatus(t, s).Resources}, setup.KindSystem, setup.SystemCBN); r == nil || len(r.Owned) != 0 {
		t.Fatalf("claims after the release: %+v", r)
	}
}
