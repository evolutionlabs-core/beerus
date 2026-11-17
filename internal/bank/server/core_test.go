package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

func registryServer(now *time.Time, transport roundTrip) *Server {
	return New(Config{
		Bank:    config.Bank{ID: "bank-a", Name: "Bank A"},
		CoreURL: "http://core.invalid",
		Link:    Link{Client: &http.Client{Transport: transport}},
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clock:   clock.New(func() time.Time { return *now }),
	})
}

func registryCache(now *time.Time, fetch func(context.Context) ([]*openbanking.RegistryClient, error)) *RegistryCache {
	return NewRegistryCache("bank-a", clock.New(func() time.Time { return *now }), slog.New(slog.NewTextHandler(io.Discard, nil)), fetch)
}

func TestRegistryRefreshServesStaleThenFailsClosed(t *testing.T) {
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, clock.Lagos)
	var calls int
	down := true
	r := registryCache(&now, func(context.Context) ([]*openbanking.RegistryClient, error) {
		calls++
		if down {
			return nil, errors.New("NIBSS is down")
		}
		return []*openbanking.RegistryClient{{ID: "tpp-1", CertCN: "tpp"}}, nil
	})
	ctx := context.Background()
	steps := []struct {
		name    string
		advance time.Duration
		down    bool
		calls   int
		failed  bool
	}{
		{"first read with NIBSS down", 0, true, 1, true},
		{"NIBSS up", 2 * refreshFailFast, false, 2, false},
		{"fresh copy needs no call", registryTTL - time.Second, false, 2, false},
		{"stale copy after the TTL", 2 * time.Second, true, 3, false},
		{"still served inside the stale limit", registryStale - 2*registryTTL, true, 4, false},
		{"fails closed past the stale limit", registryTTL, true, 5, true},
		{"fails fast after a failure", refreshFailFast / 2, false, 5, true},
		{"recovers when NIBSS is back", 2 * refreshFailFast, false, 6, false},
	}
	for _, step := range steps {
		now, down = now.Add(step.advance), step.down
		err := r.Refresh(ctx)
		if (err != nil) != step.failed || calls != step.calls {
			t.Fatalf("%s: error %v, %d calls; want failed %v and %d calls", step.name, err, calls, step.failed, step.calls)
		}
	}
	if c, ok := r.RegistryClient("tpp-1"); !ok || c.CertCN != "tpp" {
		t.Fatalf("client tpp-1 after recovery: %+v, %v", c, ok)
	}
	if c, ok := r.RegistryClientByCert("tpp"); !ok || c.ID != "tpp-1" {
		t.Fatalf("client by certificate after recovery: %+v, %v", c, ok)
	}
}

func TestSlowRegistryFetchIsSharedAndHoldsNoLock(t *testing.T) {
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, clock.Lagos)
	in, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	r := registryCache(&now, func(context.Context) ([]*openbanking.RegistryClient, error) {
		calls.Add(1)
		in <- struct{}{}
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
	if !r.mu.TryLock() {
		t.Fatal("the registry lock is held across the call to NIBSS")
	}
	r.mu.Unlock()
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("%d calls reached NIBSS for %d waiting requests", calls.Load(), waiting)
	}
}

func TestNIBSSClientRegistryAndConsents(t *testing.T) {
	var seen []string
	var body string
	c := NIBSSClient{Base: "https://nibss.invalid", HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		switch {
		case r.URL.Path == "/v1/consents" && r.Header.Get("Content-Type") == "application/json":
			raw, _ := io.ReadAll(r.Body)
			body = string(raw)
			if strings.Contains(body, `"source_seq":2`) {
				return stubReply(422, `{"error":{"code":"idempotency_key_reused","message":"Source sequence 2 of bank bank-a was first recorded with other status.","request_id":"req_1"}}`), nil
			}
			return stubReply(200, `{}`), nil
		case r.URL.Query().Get("cursor") == "":
			return stubReply(200, `{"data":[{"id":"OBR-1","cert_cn":"one"}],"next_cursor":"OBR-1"}`), nil
		default:
			return stubReply(200, `{"data":[{"id":"OBR-2","cert_cn":"two"}]}`), nil
		}
	})}}
	ctx := context.Background()
	got, err := c.RegistryClients(ctx)
	if err != nil || len(got) != 2 || got[0].ID != "OBR-1" || got[1].CertCN != "two" {
		t.Fatalf("registry: %+v, %v", got, err)
	}
	want := []string{"GET /v1/registry/clients?limit=500", "GET /v1/registry/clients?cursor=OBR-1&limit=500"}
	if !slices.Equal(seen, want) {
		t.Fatalf("requests %v, want %v", seen, want)
	}
	at := time.Date(2026, 11, 14, 10, 30, 0, 0, clock.Lagos)
	e := openbanking.ConsentEvent{Seq: 1, ConsentID: "obc_1", ClientID: "OBR-1", PartnerID: "balance", Status: openbanking.Revoked, Via: "bank", At: at}
	if err := c.RecordConsent(ctx, "db1", e); err != nil {
		t.Fatal(err)
	}
	wantBody := `{"incarnation":"db1","source_seq":1,"consent_id":"obc_1","client_id":"OBR-1","partner_id":"balance","status":"Revoked","via":"bank","changed_at":"2026-11-14T10:30:00+01:00"}`
	if body != wantBody {
		t.Fatalf("consent body %s, want %s", body, wantBody)
	}
	e.Seq = 2
	err = c.RecordConsent(ctx, "db1", e)
	if err == nil || err.Error() != "record consent obc_1 at NIBSS: Source sequence 2 of bank bank-a was first recorded with other status." {
		t.Fatalf("reused source seq: %v", err)
	}
}

func TestDirectoryOffersOnlyBanksThatTakeTransfers(t *testing.T) {
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, clock.Lagos)
	s := registryServer(&now, nil)
	for _, in := range []struct{ id, status string }{{"bank-a", "active"}, {"bank-b", "suspended"}, {"bank-c", "terminating"}, {"bank-d", "active"}} {
		s.dirBanks = append(s.dirBanks, directoryBank(nip.Institution{ID: in.id, Name: in.id, Status: in.status}))
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
	if own := s.Lookup("bank-a"); own == nil || own.Name != "Bank A" {
		t.Fatalf("a bank's own entry must come from its own config, got %+v", own)
	}
}
