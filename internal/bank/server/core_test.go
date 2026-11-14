package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nip"
)

func registryServer(now *time.Time, transport roundTrip) *Server {
	return New(Settings{
		Bank:    config.Bank{ID: "bank-a", Name: "Bank A"},
		CoreURL: "http://core.invalid",
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clock:   clock.New(func() time.Time { return *now }),
		Link:    Link{Client: &http.Client{Transport: transport}},
	})
}

func TestRegistryRefreshServesStaleThenFailsClosed(t *testing.T) {
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, clock.Lagos)
	var calls int
	down := true
	s := registryServer(&now, func(r *http.Request) (*http.Response, error) {
		calls++
		if down {
			return stubReply(503, `{}`), nil
		}
		return stubReply(200, `{"data":[{"id":"tpp-1"}]}`), nil
	})
	ctx := context.Background()
	steps := []struct {
		name    string
		advance time.Duration
		down    bool
		calls   int
		failed  bool
	}{
		{"first read with core down", 0, true, 1, true},
		{"core up", 2 * refreshFailFast, false, 2, false},
		{"fresh copy needs no call", registryTTL - time.Second, false, 2, false},
		{"stale copy after the TTL", 2 * time.Second, true, 3, false},
		{"still served inside the stale limit", registryStale - 2*registryTTL, true, 4, false},
		{"fails closed past the stale limit", registryTTL, true, 5, true},
		{"fails fast after a failure", refreshFailFast / 2, false, 5, true},
		{"recovers when core is back", 2 * refreshFailFast, false, 6, false},
	}
	for _, step := range steps {
		now, down = now.Add(step.advance), step.down
		err := s.RefreshRegistry(ctx)
		if (err != nil) != step.failed || calls != step.calls {
			t.Fatalf("%s: error %v, %d calls; want failed %v and %d calls", step.name, err, calls, step.failed, step.calls)
		}
	}
	if len(s.registry) != 1 || s.registry["tpp-1"] == nil {
		t.Fatalf("registry %v after recovery", s.registry)
	}
}

func TestSlowCoreFetchIsSharedAndHoldsNoLock(t *testing.T) {
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, clock.Lagos)
	in, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	s := registryServer(&now, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		in <- struct{}{}
		<-release
		return stubReply(200, `{"data":[]}`), nil
	})
	const waiting = 5
	errs := make(chan error, waiting)
	var wg sync.WaitGroup
	for range waiting {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- s.RefreshRegistry(context.Background())
		}()
	}
	<-in
	for name, lock := range map[string]*sync.Mutex{"registry": &s.regMu, "directory": &s.dirMu, "bank": &s.mu} {
		if !lock.TryLock() {
			t.Fatalf("the %s lock is held across the call to core", name)
		}
		lock.Unlock()
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("%d calls reached core for %d waiting requests", calls.Load(), waiting)
	}
}

func TestDirectoryOffersOnlyBanksThatTakeTransfers(t *testing.T) {
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, clock.Lagos)
	s := registryServer(&now, nil)
	for _, in := range []struct{ id, status string }{{"bank-a", "active"}, {"bank-b", "suspended"}, {"bank-c", "terminating"}, {"bank-d", "active"}} {
		s.dirBanks = append(s.dirBanks, DirectoryBank(nip.Institution{ID: in.id, Name: in.id, Status: in.status}))
	}
	var got []string
	for _, b := range s.Directory() {
		if !b.Closed() {
			got = append(got, b.ID)
		}
	}
	if len(got) != 3 || got[0] != "bank-a" || got[1] != "bank-b" || got[2] != "bank-d" {
		t.Fatalf("offered %v, want bank-a, bank-b and bank-d without the terminating bank-c", got)
	}
	if own := s.lookup("bank-a"); own == nil || own.Name != "Bank A" {
		t.Fatalf("a bank's own entry must come from its own config, got %+v", own)
	}
}
