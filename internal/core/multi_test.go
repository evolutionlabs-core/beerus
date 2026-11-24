package core

import (
	"github.com/evolutionlabs-core/beerus/internal/control"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/config"
)

func TestControlOnlyAnswersItsOwnHostsAndPeers(t *testing.T) {
	for _, c := range []struct {
		bind, host, remote string
		want               int
	}{
		{"0.0.0.0", "sim:7480", "10.1.2.3:5555", 204},
		{"0.0.0.0", "127.0.0.1:7480", "10.1.2.3:5555", 204},
		{"0.0.0.0", "evil.example:7480", "10.1.2.3:5555", 403},
		{"127.0.0.1", "sim:7480", "10.1.2.3:5555", 403},
		{"127.0.0.1", "sim:7480", "127.0.0.1:5555", 204},
	} {
		a := &App{link: control.Link{Bind: c.bind, Token: "ctl_secret"}, cfg: &config.Config{Connect: config.Connect{ControlPort: 7480}}, coreURL: "http://sim:7480"}
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
