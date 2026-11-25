package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func stubReply(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": {"application/json"}}}
}

func registryCache(fetch func(context.Context) ([]*openbanking.RegistryClient, error)) *RegistryCache {
	return NewRegistryCache("bank-a", clock.New(), slog.New(slog.NewTextHandler(io.Discard, nil)), fetch)
}

func TestRegistryRefreshServesStaleThenFailsClosed(t *testing.T) {
	var calls int
	down := true
	r := registryCache(func(context.Context) ([]*openbanking.RegistryClient, error) {
		calls++
		if down {
			return nil, errors.New("NIBSS is down")
		}
		return []*openbanking.RegistryClient{{ID: "tpp-1", CertCN: "tpp"}}, nil
	})
	for _, s := range []struct {
		name      string
		cached    time.Duration
		down      bool
		calls     int
		failed    bool
		retry     bool
		failedAgo time.Duration
	}{
		{"first read with NIBSS down", 0, true, 1, true, true, 0},
		{"fails fast after a failure", 0, false, 1, true, false, 0},
		{"recovers once fail fast has passed", 0, false, 2, false, false, refreshFailFast + time.Second},
		{"fresh copy needs no call", 0, false, 2, false, false, 0},
		{"a second before the TTL ends", registryTTL - time.Second, false, 2, false, false, 0},
		{"stale copy after the TTL", registryTTL + time.Second, true, 3, false, true, 0},
		{"still served inside the stale limit", registryStale - time.Second, true, 4, false, true, 0},
		{"fails closed past the stale limit", registryStale + time.Second, true, 5, true, true, 0},
	} {
		if s.retry {
			r.flight = refresher{}
		}
		r.mu.Lock()
		if s.cached > 0 {
			r.at = r.clock.Real().Add(-s.cached)
		}
		r.mu.Unlock()
		r.flight.mu.Lock()
		if s.failedAgo > 0 {
			r.flight.failedAt = r.clock.Real().Add(-s.failedAgo)
		}
		r.flight.mu.Unlock()
		down = s.down
		if err := r.Refresh(context.Background()); (err != nil) != s.failed || calls != s.calls {
			t.Fatalf("%s: error %v, %d calls; want failed %v and %d calls", s.name, err, calls, s.failed, s.calls)
		}
	}
	c, ok := r.RegistryClient("tpp-1")
	byCaller, ok2 := r.RegistryClientByCaller("tpp")
	if !ok || !ok2 || c.CertCN != "tpp" || byCaller.ID != "tpp-1" {
		t.Fatalf("clients after recovery: %+v %+v", c, byCaller)
	}
}

func TestSlowRegistryFetchIsSharedAndHoldsNoLock(t *testing.T) {
	in, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	r := registryCache(func(ctx context.Context) ([]*openbanking.RegistryClient, error) {
		calls.Add(1)
		select {
		case in <- struct{}{}:
		case <-ctx.Done():
		}
		<-release
		return nil, nil
	})
	const waiting = 5
	errs := make(chan error, waiting)
	var wg sync.WaitGroup
	for range waiting {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- r.Refresh(context.Background())
		}()
	}
	<-in
	deadline := time.After(time.Second)
	for !r.mu.TryLock() {
		select {
		case <-deadline:
			t.Fatal("the registry lock is held across the call to NIBSS")
		default:
			runtime.Gosched()
		}
	}
	r.mu.Unlock()
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		chk(t, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("%d calls reached NIBSS for %d waiting requests", calls.Load(), waiting)
	}
}

func TestNIBSSClientRegistryAndConsents(t *testing.T) {
	var seen []string
	var body string
	c := NIBSSClient{Base: "http://nibss.invalid", ID: "bank-a", HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get(config.CallerHeader); got != "bank-a" {
			t.Errorf("%s = %q, want bank-a", config.CallerHeader, got)
		}
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		switch {
		case r.URL.Path == "/v1/consents":
			raw, _ := io.ReadAll(r.Body)
			if body = string(raw); strings.Contains(body, `"source_seq":2`) {
				return stubReply(422, `{"error":{"code":"idempotency_key_reused","message":"Source sequence 2 of bank bank-a was first recorded with other status.","request_id":"req_1"}}`), nil
			}
			return stubReply(200, `{}`), nil
		case r.URL.Query().Get("cursor") == "":
			return stubReply(200, `{"data":[{"id":"OBR-1","cert_cn":"one"}],"next_cursor":"OBR-1"}`), nil
		}
		return stubReply(200, `{"data":[{"id":"OBR-2","cert_cn":"two"}]}`), nil
	})}}
	ctx := context.Background()
	got, err := c.RegistryClients(ctx)
	if err != nil || len(got) != 2 || got[0].ID != "OBR-1" || got[1].CertCN != "two" || !slices.Equal(seen, []string{"GET /v1/registry/clients?limit=500", "GET /v1/registry/clients?cursor=OBR-1&limit=500"}) {
		t.Fatalf("registry: %+v, %v, requests %v", got, err, seen)
	}
	e := openbanking.ConsentEvent{Seq: 1, ConsentID: "obc_1", ClientID: "OBR-1", PartnerID: "balance", Status: openbanking.Revoked, Via: "bank", At: time.Date(2026, 11, 14, 10, 30, 0, 0, clock.Lagos)}
	chk(t, c.RecordConsent(ctx, "db1", e))
	if want := `{"incarnation":"db1","source_seq":1,"consent_id":"obc_1","client_id":"OBR-1","partner_id":"balance","status":"Revoked","via":"bank","changed_at":"2026-11-14T10:30:00+01:00"}`; body != want {
		t.Fatalf("consent body %s, want %s", body, want)
	}
	e.Seq = 2
	if err := c.RecordConsent(ctx, "db1", e); err == nil || err.Error() != "record consent obc_1 at NIBSS: Source sequence 2 of bank bank-a was first recorded with other status." {
		t.Fatalf("reused source seq: %v", err)
	}
}

func TestDirectoryOffersOnlyBanksThatTakeTransfers(t *testing.T) {
	s := New(Config{Bank: config.Bank{ID: "bank-a", Name: "Bank A"}, CoreURL: "http://core.invalid", Link: control.Link{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Clock: clock.New()})
	for _, in := range []struct{ id, status string }{{"bank-a", "active"}, {"bank-b", "suspended"}, {"bank-c", "terminating"}, {"bank-d", "active"}} {
		s.dirBanks = append(s.dirBanks, directoryBank(nip.Institution{ID: in.id, Name: in.id, Status: in.status}))
	}
	var got []string
	for _, b := range s.Directory() {
		if !b.Closed() {
			got = append(got, b.ID)
		}
	}
	if own := s.Lookup("bank-a"); !slices.Equal(got, []string{"bank-a", "bank-b", "bank-d"}) || own == nil || own.Name != "Bank A" {
		t.Fatalf("offered %v, own entry %+v", got, own)
	}
}
