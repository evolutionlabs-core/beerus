package setup

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/config"
)

const sampleLocal = `database_url: postgres:///postgres?host=/tmp
bind: 127.0.0.1
ports: {api_offset: 10, window: 300}
connect:
  host: localhost
  partner_port: 7443
  widget_port: 7444
  control_port: 7480
  health_port: 7490
  sync_interval: 30s
  refresh_min_interval: 5m
partners:
  balance: {redirect_uris: [http://localhost:3000/callback], webhook_url: http://localhost:3000/webhooks}
`

func writeLocal(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "local.yaml"), []byte(body), 0o600))
	return dir
}

func TestLoadLocal(t *testing.T) {
	t.Setenv(config.DatabaseURLEnv, "")
	dir := writeLocal(t, sampleLocal)
	l, err := LoadLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if l.DatabaseURL != "postgres:///postgres?host=/tmp" || l.Bind != "127.0.0.1" || l.Ports.Window != 300 || l.Connect.ControlPort != 7480 || l.Connect.RefreshEvery != "5m" {
		t.Fatalf("%+v", l)
	}
	if p := l.Partners["balance"]; p.WebhookURL != "http://localhost:3000/webhooks" || !slices.Equal(p.RedirectURIs, []string{"http://localhost:3000/callback"}) {
		t.Fatalf("partner: %+v", p)
	}
	t.Setenv(config.DatabaseURLEnv, "postgres://other:5432/postgres")
	if l, err = LoadLocal(dir); err != nil || l.DatabaseURL != "postgres://other:5432/postgres" {
		t.Fatalf("env override: %+v %v", l, err)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "local.yaml")); err != nil || string(raw) != sampleLocal {
		t.Fatalf("local.yaml changed: %v\n%s", err, raw)
	}
	bare := writeLocal(t, strings.Replace(sampleLocal, "database_url: postgres:///postgres?host=/tmp\n", "", 1))
	if l, err = LoadLocal(bare); err != nil || l.DatabaseURL != "postgres://other:5432/postgres" {
		t.Fatalf("env fills a missing url: %+v %v", l, err)
	}
	if _, err := LoadLocal(t.TempDir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file: got %v", err)
	}
}

func TestLoadLocalRejects(t *testing.T) {
	t.Setenv(config.DatabaseURLEnv, "")
	cases := []struct {
		name, old, new, want string
	}{
		{"no url", "database_url: postgres:///postgres?host=/tmp\n", "", "local.yaml:1:1: database_url is required. Set it in local.yaml or in BEERUS_DATABASE_URL."},
		{"typo", "database_url:", "databse_url:", `local.yaml:1:1: unknown field "databse_url" (did you mean "database_url"?)`},
		{"bad bind", "bind: 127.0.0.1", "bind: nowhere", `local.yaml:2:1: bind "nowhere" is not an IP address. Use an address such as 127.0.0.1.`},
		{"port too high", "partner_port: 7443", "partner_port: 70000", "local.yaml:6:3: connect.partner_port 70000 is not a port. Use a number from 1 to 65535, or leave it out."},
		{"zero window", "window: 300", "window: 0", "local.yaml:3:25: ports.window 0 must be more than zero. Leave it out for the default."},
		{"bad duration", "sync_interval: 30s", "sync_interval: soon", `local.yaml:10:3: connect.sync_interval "soon" is not a duration longer than zero. Use a duration such as 30s.`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(sampleLocal, c.old) {
				t.Fatalf("sample has no %q", c.old)
			}
			_, err := LoadLocal(writeLocal(t, strings.Replace(sampleLocal, c.old, c.new, 1)))
			if err == nil || err.Error() != c.want {
				t.Fatalf("got  %v\nwant %s", err, c.want)
			}
		})
	}
}
