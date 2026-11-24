package site

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/banktest"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/view"
)

type capture struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (c *capture) Header() http.Header         { return c.header }
func (c *capture) Write(p []byte) (int, error) { return c.body.Write(p) }
func (c *capture) WriteHeader(status int)      { c.status = status }

func TestAccessHistoryPagesAreBounded(t *testing.T) {
	rows := []struct {
		page   string
		want   int
		ok     bool
		status int
		body   string
	}{
		{"", 1, true, 0, ""},
		{"1000", 1000, true, 0, ""},
		{"1001", 0, false, http.StatusUnprocessableEntity, "The page 1001 is too high. The access history allows pages 1 to 1000.\n"},
		{"300000000000000000", 0, false, http.StatusUnprocessableEntity, "The page 300000000000000000 is too high. The access history allows pages 1 to 1000.\n"},
		{"abc", 0, false, http.StatusUnprocessableEntity, "The page \"abc\" must be a whole number.\n"},
	}
	for _, r := range rows {
		w := &capture{header: http.Header{}}
		req := &http.Request{URL: &url.URL{Path: "/apps", RawQuery: "page=" + r.page}}
		page, ok := view.PageParam(w, req, "access history", maxHistoryPages)
		if ok != r.ok || w.status != r.status || w.body.String() != r.body || (ok && page != r.want) {
			t.Errorf("page %q gave %d, %v, status %d, body %q", r.page, page, ok, w.status, w.body.String())
		}
	}
}

type fakeHost struct {
	server.Host
	phase      string
	refreshErr error
	books      *bank.Bank
}

func (f fakeHost) Books() (*bank.Bank, error) {
	return f.books, nil
}

func newBooks(t *testing.T) *bank.Bank {
	t.Helper()
	books := bank.New(ledger.New(banktest.Schema(t), nil), bank.Profile{ID: "bank-a", Name: "Bank A", NIPCode: "000014", NUBANCode: "014"}, banktest.Fees(t))
	if err := books.Setup(context.Background()); err != nil {
		t.Fatal(err)
	}
	return books
}

func (f fakeHost) Bank() config.Bank {
	return config.Bank{ID: "bank-a", Name: "Bank A", Port: 7000}
}

func (f fakeHost) Phase() (string, string) {
	return f.phase, ""
}

func (f fakeHost) RefreshRegistry(context.Context) error {
	return f.refreshErr
}

func TestGuardOrder(t *testing.T) {
	const own = "bank-a.localhost:7000"
	rows := []struct {
		name    string
		phase   string
		refresh error
		method  string
		path    string
		host    string
		origin  string
		want    int
	}{
		{name: "an active bank serves the login page", phase: config.PhaseActive, method: "GET", path: "/login", host: own, want: http.StatusOK},
		{name: "a foreign Host is refused", phase: config.PhaseActive, method: "GET", path: "/login", host: "evil.example:7000", want: http.StatusForbidden},
		{name: "health answers before the Host check", phase: config.PhaseActive, method: "GET", path: "/health", host: "evil.example:7000", want: http.StatusOK},
		{name: "the Host check comes before the closed check", phase: config.PhaseClosed, method: "GET", path: "/login", host: "evil.example:7000", want: http.StatusForbidden},
		{name: "internal calls skip the closed check and need a token", phase: config.PhaseClosed, method: "POST", path: "/internal/revoke", host: own, want: http.StatusUnauthorized},
		{name: "v1 calls skip the suspended check", phase: config.PhaseSuspended, method: "GET", path: "/v1/health", host: own, want: http.StatusNotFound},
		{name: "a foreign Host cannot reach v1", phase: config.PhaseActive, method: "GET", path: "/v1/health", host: "evil.example:7000", want: http.StatusForbidden},
		{name: "a closed bank answers 409", phase: config.PhaseClosed, method: "GET", path: "/login", host: own, want: http.StatusConflict},
		{name: "a closed bank still serves assets", phase: config.PhaseClosed, method: "GET", path: "/assets/theme.js", host: own, want: http.StatusOK},
		{name: "a suspended bank answers 503", phase: config.PhaseSuspended, method: "GET", path: "/login", host: own, want: http.StatusServiceUnavailable},
		{name: "a suspended bank still serves assets", phase: config.PhaseSuspended, method: "GET", path: "/assets/theme.js", host: own, want: http.StatusOK},
		{name: "suspended comes before the Origin check", phase: config.PhaseSuspended, method: "POST", path: "/login", host: own, origin: "http://evil.example", want: http.StatusServiceUnavailable},
		{name: "a cross-origin post is refused", phase: config.PhaseActive, method: "POST", path: "/login", host: own, origin: "http://evil.example", want: http.StatusForbidden},
		{name: "authorize stops when the registry cannot be read", phase: config.PhaseActive, refresh: errors.New("down"), method: "GET", path: "/authorize", host: own, want: http.StatusServiceUnavailable},
		{name: "authorize goes on when the registry is read", phase: config.PhaseActive, method: "GET", path: "/authorize", host: own, want: http.StatusBadRequest},
	}
	books := newBooks(t)
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			h := New(Config{Host: fakeHost{phase: r.phase, refreshErr: r.refresh, books: books}, Clock: clock.New(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			srv := &http.Server{Handler: h}
			done := make(chan struct{})
			go func() {
				defer close(done)
				_ = srv.Serve(ln)
			}()
			t.Cleanup(func() {
				_ = srv.Close()
				<-done
			})
			req, err := http.NewRequest(r.method, "http://"+ln.Addr().String()+r.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Host = r.host
			if r.origin != "" {
				req.Header.Set("Origin", r.origin)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != r.want {
				t.Fatalf("%s %s answered %d, want %d", r.method, r.path, resp.StatusCode, r.want)
			}
		})
	}
}
