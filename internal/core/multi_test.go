package core

import (
	"github.com/evolutionlabs-core/beerus/internal/control"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/config"
)

func testBankConfig() *config.Config {
	return &config.Config{Connect: config.Connect{ControlPort: 7480}, Banks: []*config.Bank{{ID: "bank-a", Name: "Bank A", Type: "commercial", NIPCode: "999001", NUBANCode: "901", HistoryDays: 30, Port: 7445}}}
}

func TestControlOnlyAnswersItsOwnHostsAndPeers(t *testing.T) {
	for _, c := range []struct {
		bind, host, remote string
		want               int
	}{
		{"0.0.0.0", "sim:7480", "10.1.2.3:5555", 204},
		{"0.0.0.0", "127.0.0.1:7480", "10.1.2.3:5555", 204},
		{"0.0.0.0", "localhost:7480", "10.1.2.3:5555", 204},
		{"0.0.0.0", "evil.example:7480", "10.1.2.3:5555", 403},
		{"0.0.0.0", "sim:9999", "10.1.2.3:5555", 403},
		{"0.0.0.0", "sim", "10.1.2.3:5555", 403},
		{"127.0.0.1", "sim:7480", "10.1.2.3:5555", 403},
		{"127.0.0.1", "sim:7480", "127.0.0.1:5555", 204},
	} {
		a := &App{link: control.Link{Bind: c.bind, Token: "ctl_secret"}, cfg: testBankConfig(), coreURL: "http://sim:7480"}
		h := a.localOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		req.Host, req.RemoteAddr = c.host, c.remote
		req.Header.Set("Authorization", "Bearer ctl_secret")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("bind %s, host %q, peer %s answered %d, want %d", c.bind, c.host, c.remote, rec.Code, c.want)
		}
	}
}
