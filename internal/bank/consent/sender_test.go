package consent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/pki"
)

func TestEventBackoffGrowsAndStops(t *testing.T) {
	last := time.Duration(0)
	for n := 0; n < 40; n++ {
		d := Backoff(n)
		if d < last || d > eventBackoffAt {
			t.Fatalf("attempt %d waits %s after %s", n, d, last)
		}
		last = d
	}
	if Backoff(0) != eventBackoff || last != eventBackoffAt {
		t.Fatalf("backoff runs from %s to %s", Backoff(0), last)
	}
}

func TestEventClientIsSharedPerDestination(t *testing.T) {
	dir := t.TempDir()
	authority, err := pki.NewAuthority("test", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.Save(dir, "root"); err != nil {
		t.Fatal(err)
	}
	s := &Sender{CA: filepath.Join(dir, "root.crt")}
	a, err := s.client("https://widget-a.localhost")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		again, err := s.client("https://widget-a.localhost")
		if err != nil || again != a {
			t.Fatalf("attempt %d built a new client: %v", i, err)
		}
	}
	b, err := s.client("https://widget-b.localhost")
	if err != nil || b == a {
		t.Fatalf("another destination shares a client: %v", err)
	}
	if tr, ok := a.Transport.(*http.Transport); !ok || tr.IdleConnTimeout <= 0 {
		t.Fatalf("idle connections are never closed: %+v", a.Transport)
	}
}

type cancelledStore struct {
	*openbanking.MemStore
}

func (c cancelledStore) Delivered(ctx context.Context, id int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.MemStore.Delivered(ctx, id)
}

func (c cancelledStore) Retry(ctx context.Context, id int64, attempts int, next time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.MemStore.Retry(ctx, id, attempts, next)
}

type answer func(*http.Request) (*http.Response, error)

func (f answer) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestEventsAreMarkedAfterTheAnswer(t *testing.T) {
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, clock.Lagos)
	rows := []struct {
		name     string
		status   int
		cancel   bool
		attempts int
		pending  bool
		retryAt  time.Duration
	}{
		{name: "a 2xx answer marks the row delivered", status: http.StatusNoContent},
		{name: "a 5xx answer schedules a retry", status: http.StatusServiceUnavailable, attempts: 1, pending: true, retryAt: Backoff(0)},
		{name: "a cancelled context still marks the row delivered", status: http.StatusNoContent, cancel: true},
		{name: "a cancelled context still schedules the retry", status: http.StatusBadGateway, cancel: true, attempts: 1, pending: true, retryAt: Backoff(0)},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			dir := t.TempDir()
			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			der, err := x509.MarshalPKCS8PrivateKey(key)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(dir, "banks", "bank-a"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "banks", "bank-a", config.EventSignerName+".key"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
				t.Fatal(err)
			}
			store := cancelledStore{openbanking.NewMemStore()}
			srv := openbanking.New(openbanking.Config{BankID: "bank-a", Store: store, Real: func() time.Time { return now }, Now: func() time.Time { return now }})
			if err := store.Enqueue(t.Context(), openbanking.ConsentEvent{ConsentID: "obc_1", Bank: "bank-a", Status: openbanking.Revoked, At: now}, now); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			const widget = "https://widget.localhost"
			s := &Sender{
				Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
				Clock:     clock.New(func() time.Time { return now }),
				Dir:       dir,
				WidgetURL: widget,
				Servers:   func() map[string]*openbanking.Server { return map[string]*openbanking.Server{"bank-a": srv} },
				Bank:      func(id string) *config.Bank { return &config.Bank{ID: id, APIPort: 7001} },
				clients: map[string]*http.Client{widget: {Transport: answer(func(req *http.Request) (*http.Response, error) {
					if r.cancel {
						cancel()
					}
					return &http.Response{StatusCode: r.status, Status: http.StatusText(r.status), Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
				})}},
			}
			s.Events(ctx)
			due, err := store.Due(t.Context(), now, 10)
			if err != nil || len(due) != 0 {
				t.Fatalf("due right after the send: %v, %v", due, err)
			}
			due, err = store.Due(t.Context(), now.Add(r.retryAt), 10)
			if err != nil {
				t.Fatal(err)
			}
			if r.pending != (len(due) == 1) {
				t.Fatalf("pending %v, want %v: %v", len(due) == 1, r.pending, due)
			}
			if r.pending && due[0].Attempts != r.attempts {
				t.Fatalf("attempts %d, want %d", due[0].Attempts, r.attempts)
			}
		})
	}
}
