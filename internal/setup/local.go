package setup

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
)

const (
	localFile = "local.yaml"
	maxPort   = 65535
)

func LoadLocal(dir string) (Local, error) {
	path := filepath.Join(dir, localFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		return Local{}, fmt.Errorf("read %s: %w", localFile, err)
	}
	var l Local
	d := &nodeDecoder{file: localFile, removed: map[string]string{"pace": ""}, pos: map[string]Pos{}}
	d.document(raw, reflect.ValueOf(&l).Elem())
	if len(d.errs) > 0 && len(d.pos) == 0 {
		return Local{}, errors.Join(d.errs...)
	}
	if u := os.Getenv(config.DatabaseURLEnv); u != "" {
		l.DatabaseURL = u
	}
	for _, p := range l.check(d.pos) {
		if !d.failedUnder(p.path) {
			d.errs = append(d.errs, p.err)
		}
	}
	if len(d.errs) > 0 {
		return Local{}, errors.Join(d.errs...)
	}
	return l, nil
}

type localChecker struct {
	pos      map[string]Pos
	problems []specProblem
}

func (c *localChecker) add(path, format string, args ...any) {
	c.problems = append(c.problems, specProblem{path: path, err: posErrorf(c.pos, path, format, args...)})
}

func (c *localChecker) has(path string) bool {
	_, ok := c.pos[path]
	return ok
}

func (c *localChecker) port(path string, v int) {
	if v < 0 || v > maxPort || (v == 0 && c.has(path)) {
		c.add(path, "%s %d is not a port. Use a number from 1 to %d, or leave it out", path, v, maxPort)
	}
}

func (c *localChecker) duration(path, v string) {
	if v == "" && !c.has(path) {
		return
	}
	if d, err := time.ParseDuration(v); err != nil || d <= 0 {
		c.add(path, "%s %q is not a duration longer than zero. Use a duration such as 30s", path, v)
	}
}

func (l Local) check(pos map[string]Pos) []specProblem {
	c := &localChecker{pos: pos}
	if l.DatabaseURL == "" {
		c.add("database_url", "database_url is required. Set it in %s or in %s", localFile, config.DatabaseURLEnv)
	}
	if (l.Bind != "" || c.has("bind")) && net.ParseIP(l.Bind) == nil {
		c.add("bind", "bind %q is not an IP address. Use an address such as 127.0.0.1", l.Bind)
	}
	c.port("ports.api_offset", l.Ports.APIOffset)
	if l.Ports.Window < 0 || (l.Ports.Window == 0 && c.has("ports.window")) {
		c.add("ports.window", "ports.window %d must be more than zero. Leave it out for the default", l.Ports.Window)
	}
	cn := l.Connect
	c.port("connect.partner_port", cn.PartnerPort)
	c.port("connect.widget_port", cn.WidgetPort)
	c.port("connect.control_port", cn.ControlPort)
	c.port("connect.health_port", cn.HealthPort)
	c.duration("connect.sync_interval", cn.SyncInterval)
	c.duration("connect.refresh_min_interval", cn.RefreshEvery)
	c.duration("connect.pause_lease", cn.PauseLease)
	ids := make([]string, 0, len(l.Partners))
	for id := range l.Partners {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		p := l.Partners[id]
		base := "partners[" + id + "]"
		for i, u := range p.RedirectURIs {
			if !specWebURL(u) {
				c.add(fmt.Sprintf("%s.redirect_uris[%d]", base, i), "%s.redirect_uris %q is not an http or https URL. Use a full URL such as http://localhost:3000/callback", base, u)
			}
		}
		if (p.WebhookURL != "" || c.has(base+".webhook_url")) && !specWebURL(p.WebhookURL) {
			c.add(base+".webhook_url", "%s.webhook_url %q is not an http or https URL. Use a full URL such as http://localhost:3000/webhooks", base, p.WebhookURL)
		}
	}
	return c.problems
}
