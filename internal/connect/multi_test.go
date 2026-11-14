package connect

import (
	"context"
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/evolutionlabs-core/beerus/internal/config"
)

func testBankConfig() *config.Config {
	return &config.Config{Connect: config.Connect{ControlPort: 7480}, Banks: []*config.Bank{{ID: "bank-a", Name: "Bank A", Type: "commercial", NIPCode: "999001", NUBANCode: "901", HistoryDays: 30, Port: 7445}}}
}

func TestControlHostListInMultiProcessMode(t *testing.T) {
	a := &App{bind: "0.0.0.0", cfg: testBankConfig(), ctlToken: "ctl_secret", coreURL: "http://sim:7480"}
	h := a.localOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for host, want := range map[string]int{"sim:7480": 204, "127.0.0.1:7480": 204, "localhost:7480": 204, "evil.example:7480": 403, "sim:9999": 403, "sim": 403} {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		req.Host, req.RemoteAddr = host, "10.1.2.3:5555"
		req.Header.Set("Authorization", "Bearer ctl_secret")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Host %q answered %d, want %d", host, rec.Code, want)
		}
	}
}

func TestControlAcceptsOnlyLoopbackPeersWhenBoundToLoopback(t *testing.T) {
	cases := []struct {
		bind, remote string
		want         int
	}{{"127.0.0.1", "10.1.2.3:5555", 403}, {"127.0.0.1", "127.0.0.1:5555", 204}, {"0.0.0.0", "10.1.2.3:5555", 204}}
	for _, c := range cases {
		a := &App{bind: c.bind, cfg: testBankConfig(), ctlToken: "ctl_secret", coreURL: "http://sim:7480"}
		h := a.localOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		req.Host, req.RemoteAddr = "sim:7480", c.remote
		req.Header.Set("Authorization", "Bearer ctl_secret")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("bind %s, peer %s answered %d, want %d", c.bind, c.remote, rec.Code, c.want)
		}
	}
}

func TestPeerAnswerOverTheLimitFailsClearly(t *testing.T) {
	big := bytes.Repeat([]byte(" "), maxPeerBody+1)
	a := &App{coreURL: "http://core.invalid", peers: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(big)), Header: http.Header{}}, nil
	})}}
	var out struct{ Data []*RegistryClient }
	if _, err := a.coreDo(context.Background(), http.MethodGet, "/internal/registry", nil, &out); err == nil || !strings.Contains(err.Error(), "larger than 16 MiB") {
		t.Fatalf("an oversized answer: %v", err)
	}
	a.peers = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(iotest.ErrReader(errors.New("connection reset"))), Header: http.Header{}}, nil
	})}
	if _, err := a.coreDo(context.Background(), http.MethodGet, "/internal/registry", nil, &out); err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("a broken answer: %v", err)
	}
}
