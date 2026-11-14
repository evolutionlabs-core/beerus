package consent

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

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
