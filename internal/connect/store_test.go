package connect

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/evolutionlabs-core/beerus/internal/bank/banktest"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func testStore(t *testing.T) *ctlStore {
	t.Helper()
	s, err := createStore(t.TempDir(), setup.Status{Version: setup.StatusVersion, Setup: "beerus", SetupKey: 9})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func diskStatus(t *testing.T, s *ctlStore) setup.Status {
	t.Helper()
	st, err := setup.LoadStatus(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestStoreClaimIsIdempotent(t *testing.T) {
	s := testStore(t)
	db := setup.Owned{Kind: ownedDatabase, Name: "beerus_x_bank_a"}
	port := setup.Owned{Kind: ownedPort, Name: "7445"}
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
	if err := s.Release(setup.Owned{Kind: ownedPort, Name: "9999"}); err != nil {
		t.Fatalf("releasing an unknown name: %v", err)
	}
	if got := diskStatus(t, s).Resources[0].Owned; !slices.Equal(got, []setup.Owned{db}) {
		t.Fatalf("after release: %+v", got)
	}
	if err := s.forget(setup.KindBank, "bank-a"); err != nil {
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
			r := ensureResource(st, setup.KindBank, "bank-a")
			r.Owned = append(r.Owned, setup.Owned{Kind: ownedPort, Name: name})
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
			r := ensureResource(st, setup.KindBank, "bank-a")
			r.Owned = append(r.Owned, setup.Owned{Kind: ownedPort, Name: name})
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
			if err := s.Claim(setup.KindBank, "bank-a", setup.Owned{Kind: ownedPort, Name: string(rune('a' + i))}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := diskStatus(t, s).Resources[0].Owned; len(got) != 20 {
		t.Fatalf("%d claims saved, want 20", len(got))
	}
}

func TestReadOnlyStoreRefusesWrites(t *testing.T) {
	s := testStore(t)
	ro, err := readStore(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ro.Claim(setup.KindBank, "bank-a", setup.Owned{Kind: ownedPort, Name: "1"}); !errors.Is(err, errStoreReadOnly) {
		t.Fatalf("claim from a read-only store: %v", err)
	}
	if ro.setupKey() != 9 {
		t.Fatalf("setup key %d", ro.setupKey())
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

func TestNewSetupKeyIsRandom(t *testing.T) {
	a, err := newSetupKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err := newSetupKey()
	if err != nil {
		t.Fatal(err)
	}
	if a == 0 || a == b {
		t.Fatalf("keys %d and %d", a, b)
	}
}

func initTestConfig(t *testing.T, today clock.Date) *config.Config {
	t.Helper()
	dir := t.TempDir()
	local := DefaultLocal()
	if u := os.Getenv("BEERUS_TEST_DATABASE_URL"); u != "" {
		local.DatabaseURL = u
	}
	if err := Init(dir, banktest.Spec("beerus", today), local, today); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func newTestApp(t *testing.T, o Options) *App {
	t.Helper()
	if testing.Short() {
		t.Skip("opens bank databases; run without -short")
	}
	cfg := initTestConfig(t, clock.Date{Y: 2026, M: 11, D: 3})
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	conn, err := pgx.Connect(context.Background(), cfg.DatabaseURL)
	if err != nil {
		t.Skipf("no Postgres for app tests (%v); set BEERUS_TEST_DATABASE_URL", err)
	}
	conn.Close(context.Background())
	a, err := New(cfg, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.DropBankDatabases(); err != nil {
			t.Errorf("drop databases: %v", err)
		}
	})
	return a
}

func TestIdempotencyLocksExcludeAndAreReleased(t *testing.T) {
	a := &App{}
	var wg sync.WaitGroup
	inside := map[string]int{}
	var mu sync.Mutex
	for _, scope := range []string{"s1", "s1", "s1", "s2", "s2"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e := a.idemAcquire(scope)
			mu.Lock()
			inside[scope]++
			if inside[scope] != 1 {
				t.Errorf("two requests held %s at once", scope)
			}
			mu.Unlock()
			mu.Lock()
			inside[scope]--
			mu.Unlock()
			a.idemRelease(scope, e)
		}()
	}
	wg.Wait()
	if n := len(a.idemLocks); n != 0 {
		t.Fatalf("%d locks left after every request finished", n)
	}
	e := a.idemAcquire("s3")
	if len(a.idemLocks) != 1 {
		t.Fatalf("held lock missing: %d", len(a.idemLocks))
	}
	a.idemRelease("s3", e)
	if len(a.idemLocks) != 0 {
		t.Fatal("lock not removed on release")
	}
}

func TestWriteFilesRestoresExactlyTheSavedSet(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "partner")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"api.key": "old", "client.key": "old", "stale.crt": "old"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeFiles(dir, map[string][]byte{"api.key": []byte("new"), "client.crt": []byte("cert")}); err != nil {
		t.Fatal(err)
	}
	got, err := readFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || string(got["api.key"]) != "new" || string(got["client.crt"]) != "cert" {
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
