package setup

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
  widget_tls: true
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
	if err := os.WriteFile(filepath.Join(dir, "local.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadLocal(t *testing.T) {
	t.Setenv(config.DatabaseURLEnv, "")
	l, err := LoadLocal(writeLocal(t, sampleLocal))
	if err != nil {
		t.Fatal(err)
	}
	if l.DatabaseURL != "postgres:///postgres?host=/tmp" || l.Bind != "127.0.0.1" || l.Ports.Window != 300 {
		t.Fatalf("%+v", l)
	}
	if !l.Connect.WidgetTLS || l.Connect.ControlPort != 7480 || l.Connect.RefreshEvery != "5m" {
		t.Fatalf("connect: %+v", l.Connect)
	}
	if p := l.Partners["balance"]; p.WebhookURL != "http://localhost:3000/webhooks" || !slices.Equal(p.RedirectURIs, []string{"http://localhost:3000/callback"}) {
		t.Fatalf("partner: %+v", p)
	}
}

func TestLoadLocalEnvOverridesAndIsNeverSaved(t *testing.T) {
	dir := writeLocal(t, sampleLocal)
	t.Setenv(config.DatabaseURLEnv, "postgres://other:5432/postgres")
	l, err := LoadLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if l.DatabaseURL != "postgres://other:5432/postgres" {
		t.Fatalf("database_url = %s", l.DatabaseURL)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "local.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != sampleLocal {
		t.Fatalf("local.yaml changed:\n%s", raw)
	}
}

func TestLoadLocalEnvFillsAMissingURL(t *testing.T) {
	body := strings.Replace(sampleLocal, "database_url: postgres:///postgres?host=/tmp\n", "", 1)
	dir := writeLocal(t, body)
	t.Setenv(config.DatabaseURLEnv, "")
	_, err := LoadLocal(dir)
	want := "local.yaml:1:1: database_url is required. Set it in local.yaml or in BEERUS_DATABASE_URL."
	if err == nil || err.Error() != want {
		t.Fatalf("got %v", err)
	}
	t.Setenv(config.DatabaseURLEnv, "postgres://env/postgres")
	l, err := LoadLocal(dir)
	if err != nil || l.DatabaseURL != "postgres://env/postgres" {
		t.Fatalf("%+v %v", l, err)
	}
}

func TestLoadLocalMissingFile(t *testing.T) {
	_, err := LoadLocal(t.TempDir())
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("got %v", err)
	}
}

func TestLoadLocalAcceptsTheRetiredSpeedKey(t *testing.T) {
	t.Setenv(config.DatabaseURLEnv, "")
	want, err := LoadLocal(writeLocal(t, sampleLocal))
	if err != nil {
		t.Fatal(err)
	}
	for _, pace := range []string{"pace: 1\n", "pace: 60\n"} {
		got, err := LoadLocal(writeLocal(t, strings.Replace(sampleLocal, "ports:", pace+"ports:", 1)))
		if err != nil {
			t.Fatalf("%s: %v", pace, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: got %+v, want %+v", pace, got, want)
		}
	}
}

func TestLoadLocalRejects(t *testing.T) {
	t.Setenv(config.DatabaseURLEnv, "")
	cases := []struct {
		name, old, new, want string
	}{
		{"typo", "database_url:", "databse_url:", `local.yaml:1:1: unknown field "databse_url" (did you mean "database_url"?)`},
		{"bad bind", "bind: 127.0.0.1", "bind: nowhere", `local.yaml:2:1: bind "nowhere" is not an IP address. Use an address such as 127.0.0.1.`},
		{"port too high", "partner_port: 7443", "partner_port: 70000", "local.yaml:6:3: connect.partner_port 70000 is not a port. Use a number from 1 to 65535, or leave it out."},
		{"zero window", "window: 300", "window: 0", "local.yaml:3:25: ports.window 0 must be more than zero. Leave it out for the default."},
		{"bad duration", "sync_interval: 30s", "sync_interval: soon", `local.yaml:11:3: connect.sync_interval "soon" is not a duration longer than zero. Use a duration such as 30s.`},
		{"tls as text", "widget_tls: true", "widget_tls: maybe", "local.yaml:8:15: connect.widget_tls maybe is not true or false. Write true or false."},
		{"bad webhook", "webhook_url: http://localhost:3000/webhooks", "webhook_url: ftp://x", `local.yaml:14:62: partners[balance].webhook_url "ftp://x" is not an http or https URL. Use a full URL such as http://localhost:3000/webhooks.`},
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
