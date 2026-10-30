package contracttest

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/connect"
)

func (s *Stack) waitBank(id string) map[string]any {
	s.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var last map[string]any
	for time.Now().Before(deadline) {
		for _, d := range s.ctl("GET", "/banks", nil)["data"].([]any) {
			st := d.(map[string]any)
			if st["id"] == id {
				last = st
				if st["phase"] != connect.PhaseProvisioning {
					return st
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	s.t.Fatalf("bank %s still provisioning: %v", id, last)
	return nil
}

func TestCreateBank(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newStack(t)
	snap := s.ctl("POST", "/snapshots", map[string]any{"name": "before"})
	out := s.ctl("POST", "/banks", map[string]any{"name": "Abuja Digital", "type": "digital"})
	if out["id"] != "abuja-digital" || out["phase"] != connect.PhaseProvisioning {
		t.Fatalf("create: %v", out)
	}
	st := s.waitBank("abuja-digital")
	if st["phase"] != connect.PhaseActive {
		t.Fatalf("phase: %v", st)
	}
	for _, step := range st["steps"].([]any) {
		if step.(map[string]any)["done"] != true {
			t.Fatalf("step not done: %v", step)
		}
	}
	cfg, err := config.Load(s.cfg.Dir)
	if err != nil {
		t.Fatal(err)
	}
	b := cfg.Bank("abuja-digital")
	if b == nil || b.Type != "digital" || b.NIPCode == "" || b.NIPCode == "999003" || b.NIPCode == "999004" || b.Database != config.DatabaseName(cfg.Dir, "abuja-digital") {
		t.Fatalf("saved bank: %+v", b)
	}
	m, err := cfg.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	claimed := map[string]bool{}
	for _, r := range m.Resources {
		if r.Bank == "abuja-digital" {
			claimed[r.Kind] = true
		}
	}
	if !claimed["database"] || !claimed["port"] {
		t.Fatalf("manifest: %+v", m.Resources)
	}
	resp, err := http.Get(b.URL() + "/login")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "Abuja Digital") {
		t.Fatalf("online banking: %d %.200s", resp.StatusCode, body)
	}
	ob := s.bankBrowser()
	ob.base = b.URL()
	p := s.persona("adaeze")
	ob.get("/login")
	ob.post("/login", url.Values{"username": {p.Username}, "password": {p.Password}})
	if !strings.Contains(ob.last, "has no accounts at Abuja Digital") {
		t.Fatalf("sign-in at a bank with no customers: %.300s", ob.last)
	}
	ls := s.must(s.call("POST", "/v1/link_sessions", map[string]any{"start_date": s.today().String(), "workspace_id": "w", "redirect_uri": s.redirect}), 201)
	br := s.browser()
	br.get(fmt.Sprint(ls.JSON["widget_url"]))
	if !strings.Contains(br.last, "Abuja Digital") {
		t.Fatal("the widget does not list Bank C")
	}
	for _, bad := range []map[string]any{
		{"name": "Abuja Digital"},
		{"name": "Bank C"},
		{"name": "bank d microfinance"},
		{"name": "Bank E", "type": "credit-union"},
		{"name": "Bank F", "nip_code": "999003"},
		{"name": "Bank G", "nip_code": b.NIPCode},
		{"name": "Bank H", "history_days": 5000},
		{"name": "Bank I", "settles_through": "nowhere"},
	} {
		if _, err := s.ctlTry("POST", "/banks", bad); err == nil || !strings.Contains(err.Error(), "HTTP 422") {
			t.Errorf("%v: %v", bad, err)
		}
	}
	s.ctl("POST", "/snapshots/"+fmt.Sprint(snap["id"])+"/restore", nil)
	if st := s.waitBank("abuja-digital"); st["phase"] != connect.PhaseActive {
		t.Fatalf("after restore: %v", st)
	}
	resp, err = http.Get(b.URL() + "/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("online banking after restore: %d", resp.StatusCode)
	}
	s.bankSignIn("adaeze")
}

func TestSuspendBank(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newStack(t)
	conn := s.connect("chidi", "bank-b", s.today().AddDays(-10))
	st := s.ctl("POST", "/banks/bank-b/suspend", nil)
	if st["phase"] != connect.PhaseSuspended {
		t.Fatalf("suspend: %v", st)
	}
	s.syncNow()
	c := s.must(s.call("GET", "/v1/connections/"+conn.ConnectionID, nil), 200)
	if c.JSON["status"] != "institution_down" {
		t.Fatalf("status while suspended: %v", c.JSON["status"])
	}
	resp, err := http.Get(s.cfg.Bank("bank-b").URL() + "/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("online banking while suspended: %d", resp.StatusCode)
	}
	cfg, err := config.Load(s.cfg.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Bank("bank-b").Suspended() {
		t.Fatal("suspension not saved")
	}
	s.ctl("POST", "/banks/bank-b/resume", nil)
	s.syncNow()
	c = s.must(s.call("GET", "/v1/connections/"+conn.ConnectionID, nil), 200)
	if c.JSON["status"] != "active" {
		t.Fatalf("status after resume: %v", c.JSON["status"])
	}
	if _, err := s.ctlTry("POST", "/banks/nowhere/suspend", nil); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("unknown bank: %v", err)
	}
}
