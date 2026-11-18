package server

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

type fakeSource struct {
	book     Book
	err      error
	refusing bool
	taken    *int
}

func (f fakeSource) Book(string) (Book, error) {
	return f.book, f.err
}

func (f fakeSource) Empty() bool {
	return false
}

func (f fakeSource) Refusing(string) bool {
	return f.refusing
}

func (f fakeSource) TakeTimeout(string) (string, bool) {
	if f.taken != nil {
		*f.taken++
	}
	return "", false
}

type capture struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (c *capture) Header() http.Header {
	return c.header
}

func (c *capture) WriteHeader(status int) {
	c.status = status
}

func (c *capture) Write(b []byte) (int, error) {
	return c.body.Write(b)
}

func TestNIPMountAnswersByPhase(t *testing.T) {
	rows := []struct {
		name   string
		src    fakeSource
		status int
		retry  string
	}{
		{"closed bank", fakeSource{book: Book{Name: "Bank A", Phase: config.PhaseClosed}}, http.StatusConflict, ""},
		{"closed bank that left the config", fakeSource{book: Book{Name: "bank-a", Phase: config.PhaseClosed}}, http.StatusConflict, ""},
		{"unknown bank", fakeSource{err: ErrBankNotFound}, http.StatusServiceUnavailable, "30"},
		{"books not open", fakeSource{book: Book{Name: "Bank A", Phase: config.PhaseActive}}, http.StatusServiceUnavailable, "30"},
		{"refusing bank", fakeSource{book: Book{Name: "Bank A", Phase: config.PhaseActive, Bank: &bank.Bank{}}, refusing: true}, http.StatusServiceUnavailable, "30"},
	}
	for _, r := range rows {
		b := &Banking{Src: r.src, Clock: clock.New(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
		req, err := http.NewRequest(http.MethodGet, "/v1/nip/account-names/0123456789", nil)
		if err != nil {
			t.Fatal(err)
		}
		w := &capture{header: http.Header{}}
		b.NIP("bank-a").ServeHTTP(w, req)
		if w.status != r.status || w.header.Get("Retry-After") != r.retry {
			t.Errorf("%s: %d retry %q, want %d retry %q", r.name, w.status, w.header.Get("Retry-After"), r.status, r.retry)
		}
	}
}

func TestNIPFaultWaitsForTheCaller(t *testing.T) {
	taken := 0
	src := fakeSource{book: Book{Name: "Bank A", Phase: config.PhaseActive, Bank: &bank.Bank{}}, taken: &taken}
	b := &Banking{Src: src, Clock: clock.New(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	req, err := http.NewRequest(http.MethodPost, "/v1/nip/credits", nil)
	if err != nil {
		t.Fatal(err)
	}
	w := &capture{header: http.Header{}}
	b.NIP("bank-a").ServeHTTP(w, req)
	if w.status != http.StatusForbidden || taken != 0 {
		t.Fatalf("a caller without a certificate got %d and used up %d faults, want 403 and 0", w.status, taken)
	}
}
