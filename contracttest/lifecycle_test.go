package contracttest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

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
	keys := map[string]string{}
	for _, p := range s.ctl("GET", "/personas", nil)["data"].([]any) {
		for _, a := range p.(map[string]any)["accounts"].([]any) {
			m := a.(map[string]any)
			keys[fmt.Sprint(p.(map[string]any)["id"], "@", m["institution"])] = fmt.Sprint(m["key"])
		}
	}
	st := s.ctl("POST", "/banks/bank-b/suspend", nil)
	if st["phase"] != connect.PhaseSuspended {
		t.Fatalf("suspend: %v", st)
	}
	if _, err := s.ctlTry("POST", "/transfers", map[string]any{"from": keys["chidi@bank-b"], "to": keys["tunde@bank-a"], "amount": "1000"}); err == nil || !strings.Contains(err.Error(), "bank_suspended") {
		t.Fatalf("transfer out of a suspended bank: %v", err)
	}
	if _, err := s.ctlTry("POST", "/transfers", map[string]any{"from": keys["tunde@bank-a"], "to": keys["chidi@bank-b"], "amount": "1000"}); err == nil || !strings.Contains(err.Error(), "nothing was debited") {
		t.Fatalf("transfer into a suspended bank: %v", err)
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
	s.ctl("POST", "/transfers", map[string]any{"from": keys["tunde@bank-a"], "to": keys["chidi@bank-b"], "amount": "1000"})
	s.syncNow()
	c = s.must(s.call("GET", "/v1/connections/"+conn.ConnectionID, nil), 200)
	if c.JSON["status"] != "active" {
		t.Fatalf("status after resume: %v", c.JSON["status"])
	}
	if _, err := s.ctlTry("POST", "/banks/nowhere/suspend", nil); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("unknown bank: %v", err)
	}
}

func (s *Stack) waitPhase(id, phase string, within time.Duration) map[string]any {
	s.t.Helper()
	deadline := time.Now().Add(within)
	var last map[string]any
	for time.Now().Before(deadline) {
		for _, d := range s.ctl("GET", "/banks", nil)["data"].([]any) {
			st := d.(map[string]any)
			if st["id"] == id {
				last = st
				if st["phase"] == phase {
					return st
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	s.t.Fatalf("bank %s never reached %s: %v", id, phase, last)
	return nil
}

func TestBankRepairsDrift(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newStack(t)
	conn := s.connect("chidi", "bank-b", s.today().AddDays(-10))
	acct := fmt.Sprint(conn.Accounts[0]["id"])
	before := s.must(s.call("GET", "/v1/accounts/"+acct+"/balance?as_of="+s.today().AddDays(-1).String(), nil), 200).JSON["ledger_balance"]
	pc, err := pgx.ParseConfig(s.cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	pc.Database = "postgres"
	db, err := pgx.ConnectConfig(context.Background(), pc)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	dropped := time.Now()
	if _, err := db.Exec(context.Background(), "DROP DATABASE "+s.cfg.Bank("bank-b").Database+" WITH (FORCE)"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		st := s.waitPhase("bank-b", connect.PhaseActive, 30*time.Second)
		rebuilt, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(st["steps"].([]any)[2].(map[string]any)["at"]))
		if rebuilt.After(dropped) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("bank-b was not rebuilt: %v", st)
		}
		time.Sleep(200 * time.Millisecond)
	}
	s.syncNow()
	after := s.must(s.call("GET", "/v1/accounts/"+acct+"/balance?as_of="+s.today().AddDays(-1).String(), nil), 200).JSON["ledger_balance"]
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatalf("balance changed across the repair: %v then %v", before, after)
	}
	b := s.bankBrowser()
	b.base = s.cfg.Bank("bank-b").URL()
	p := s.persona("chidi")
	b.get("/login")
	b.post("/login", url.Values{"username": {p.Username}, "password": {p.Password}})
	if !strings.Contains(b.last, "verification code") {
		t.Fatalf("sign-in after repair: %.300s", b.last)
	}
}

func TestBanksFollowConfigFile(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newStack(t)
	edit := func(change func(c *config.Config)) {
		t.Helper()
		c, err := config.Load(s.cfg.Dir)
		if err != nil {
			t.Fatal(err)
		}
		change(c)
		if err := c.Save(); err != nil {
			t.Fatal(err)
		}
	}
	edit(func(c *config.Config) {
		c.Banks = append(c.Banks, &config.Bank{ID: "kano-digital", Name: "Kano Digital", Type: "digital"})
	})
	s.waitPhase("kano-digital", connect.PhaseActive, 60*time.Second)
	saved, err := config.Load(s.cfg.Dir)
	if err != nil {
		t.Fatal(err)
	}
	kb := saved.Bank("kano-digital")
	if kb == nil || kb.Port == 0 || kb.NIPCode == "" || kb.Database == "" || kb.HistoryDays != 180 {
		t.Fatalf("defaults were not written back: %+v", kb)
	}
	resp, err := http.Get(kb.URL() + "/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("online banking: %d", resp.StatusCode)
	}
	edit(func(c *config.Config) { c.Bank("kano-digital").State = "suspended" })
	s.waitPhase("kano-digital", connect.PhaseSuspended, 10*time.Second)
	edit(func(c *config.Config) { c.Bank("kano-digital").State = "" })
	s.waitPhase("kano-digital", connect.PhaseActive, 10*time.Second)
	edit(func(c *config.Config) {
		c.Banks = append(c.Banks, &config.Bank{ID: "copycat", Name: "Copycat", NIPCode: c.Bank("bank-a").NIPCode})
		c.Bank("bank-a").Name = "Renamed"
	})
	deadline := time.Now().Add(10 * time.Second)
	var warnings []any
	for time.Now().Before(deadline) && len(warnings) < 2 {
		warnings, _ = s.ctl("GET", "/banks", nil)["warnings"].([]any)
		time.Sleep(200 * time.Millisecond)
	}
	if len(warnings) != 2 || !strings.Contains(fmt.Sprint(warnings), "copycat") || !strings.Contains(fmt.Sprint(warnings), "bank-a") {
		t.Fatalf("warnings: %v", warnings)
	}
	saved, err = config.Load(s.cfg.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Bank("copycat") != nil || saved.Bank("bank-a").Name != "Bank A" {
		t.Fatal("rejected edits were not undone in beerus.yaml")
	}
}

func TestOpenAccountAtNewBank(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newStack(t)
	s.ctl("POST", "/banks", map[string]any{"name": "Abuja Digital", "type": "digital"})
	s.waitPhase("abuja-digital", connect.PhaseActive, 60*time.Second)
	acc := s.ctl("POST", "/banks/abuja-digital/accounts", map[string]any{"persona": "adaeze", "type": "current"})
	nuban := fmt.Sprint(acc["nuban"])
	if acc["owner"] != "adaeze" || len(nuban) != 10 {
		t.Fatalf("open account: %v", acc)
	}
	if _, err := s.ctlTry("POST", "/banks/abuja-digital/accounts", map[string]any{"persona": "adaeze", "type": "current"}); err == nil || !strings.Contains(err.Error(), "HTTP 422") {
		t.Fatalf("second current account: %v", err)
	}
	from := ""
	for _, p := range s.ctl("GET", "/personas", nil)["data"].([]any) {
		for _, a := range p.(map[string]any)["accounts"].([]any) {
			m := a.(map[string]any)
			if p.(map[string]any)["id"] == "adaeze" && m["institution"] == "bank-a" && m["type"] == "current" {
				from = fmt.Sprint(m["key"])
			}
		}
	}
	s.ctl("POST", "/transfers", map[string]any{"from": from, "to": fmt.Sprint(acc["key"]), "amount": "25000"})
	b := s.bankBrowser()
	b.base = s.cfg.Bank("abuja-digital").URL()
	p := s.persona("adaeze")
	b.get("/login")
	b.post("/login", url.Values{"username": {p.Username}, "password": {p.Password}})
	m := regexp.MustCompile(`verification code is <span[^>]*>(\d{6})</span>`).FindStringSubmatch(b.last)
	if m == nil {
		t.Fatalf("sign-in at the new bank: %.300s", b.last)
	}
	b.post("/verify", url.Values{"otp": {m[1]}})
	if !strings.Contains(b.last, nuban) || !strings.Contains(b.last, "25,000.00") {
		t.Fatalf("online banking does not show the funded account: %.600s", b.last)
	}
	conn := s.connect("adaeze", "abuja-digital", s.today().AddDays(-3))
	if len(conn.Accounts) != 1 || conn.Accounts[0]["account_number"] != nuban {
		t.Fatalf("connected accounts: %v", conn.Accounts)
	}
}
