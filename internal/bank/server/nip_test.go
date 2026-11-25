package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

func (f fakeSource) Book(string) (Book, error) { return f.book, f.err }

func (f fakeSource) Empty() bool { return false }

func (f fakeSource) Refusing(string) bool { return f.refusing }

func (f fakeSource) TakeTimeout(string) (string, bool) {
	if f.taken != nil {
		*f.taken++
	}
	return "", false
}

func TestNIPMountAnswers(t *testing.T) {
	taken := 0
	active := Book{Name: "Bank A", Phase: config.PhaseActive, Bank: &bank.Bank{}}
	for _, r := range []struct {
		name, method, path string
		src                fakeSource
		status             int
		retry              string
	}{
		{"closed bank", "GET", "/v1/nip/account-names/0123456789", fakeSource{book: Book{Name: "Bank A", Phase: config.PhaseClosed}}, http.StatusConflict, ""},
		{"unknown bank", "GET", "/v1/nip/account-names/0123456789", fakeSource{err: ErrBankNotFound}, http.StatusServiceUnavailable, "30"},
		{"books not open", "GET", "/v1/nip/account-names/0123456789", fakeSource{book: Book{Name: "Bank A", Phase: config.PhaseActive}}, http.StatusServiceUnavailable, "30"},
		{"refusing bank", "GET", "/v1/nip/account-names/0123456789", fakeSource{book: active, refusing: true}, http.StatusServiceUnavailable, "30"},
		{"a caller without an ID uses up no fault", "POST", "/v1/nip/credits", fakeSource{book: active, taken: &taken}, http.StatusUnauthorized, ""},
	} {
		b := &Banking{Src: r.src, Clock: clock.New(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
		w := httptest.NewRecorder()
		b.NIP("bank-a").ServeHTTP(w, httptest.NewRequest(r.method, r.path, nil))
		if w.Code != r.status || w.Header().Get("Retry-After") != r.retry || taken != 0 {
			t.Errorf("%s: %d retry %q with %d faults used, want %d retry %q and 0", r.name, w.Code, w.Header().Get("Retry-After"), taken, r.status, r.retry)
		}
	}
}
