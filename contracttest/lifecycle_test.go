package contracttest

import (
	"context"
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/world"
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
	"github.com/evolutionlabs-core/beerus/internal/setup"
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

func (s *Stack) waitResource(kind, id string, within time.Duration, done func(map[string]any) bool) map[string]any {
	s.t.Helper()
	deadline := time.Now().Add(within)
	var last map[string]any
	for time.Now().Before(deadline) {
		r, err := s.ctlTry("GET", "/resources/"+kind+"/"+id, nil)
		if err == nil {
			last = r
			if done(r) {
				return r
			}
		} else if !strings.Contains(err.Error(), "HTTP 404") {
			s.t.Fatal(err)
		} else if done(nil) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	s.t.Fatalf("%s/%s never got there: %v", kind, id, last)
	return nil
}

func (s *Stack) waitBlocked(id, reason, text string) {
	s.t.Helper()
	s.waitResource("bank", id, 60*time.Second, func(r map[string]any) bool {
		for _, c := range asList(r["conditions"]) {
			if c["type"] == setup.ConditionBlocked && c["status"] == "True" && c["reason"] == reason && strings.Contains(fmt.Sprint(c["message"]), text) {
				return true
			}
		}
		return false
	})
}

func (s *Stack) waitGone(kind, id string) {
	s.t.Helper()
	s.waitResource(kind, id, 60*time.Second, func(r map[string]any) bool { return r == nil })
}

func (s *Stack) settleUntil(id, phase string) {
	s.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		s.ctl("POST", "/nip/settle-now", nil)
		for _, d := range s.ctl("GET", "/banks", nil)["data"].([]any) {
			if st := d.(map[string]any); st["id"] == id && st["phase"] == phase {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	s.t.Fatalf("bank %s never reached %s", id, phase)
}

func (s *Stack) closeResult(id string) map[string]any {
	s.t.Helper()
	s.settleUntil(id, connect.PhaseClosed)
	res, ok := s.ctl("GET", "/resources/bank/"+id, nil)["result"].(map[string]any)
	if !ok {
		s.t.Fatalf("bank %s has no close result", id)
	}
	return res
}

func asList(v any) []map[string]any {
	out := []map[string]any{}
	items, _ := v.([]any)
	for _, x := range items {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func TestCreateBank(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newBankStack(t)
	p := s.persona("adaeze")
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
		if at, ok := step.(map[string]any)["at"]; ok && strings.HasPrefix(fmt.Sprint(at), "0001-") {
			t.Fatalf("step has no real time: %v", step)
		}
	}
	cfg, err := connect.LoadConfig(s.cfg.Dir)
	if err != nil {
		t.Fatal(err)
	}
	b := cfg.Bank("abuja-digital")
	if b == nil || b.Type != "digital" || b.NIPCode != "999003" || b.NUBANCode != "903" || b.Database != config.DatabaseName(cfg.Dir, "abuja-digital") {
		t.Fatalf("saved bank: %+v", b)
	}
	claimed := map[string]bool{}
	for _, o := range s.owned(setup.KindBank, "abuja-digital") {
		claimed[o.Kind] = true
	}
	if !claimed["database"] || !claimed["port"] {
		t.Fatalf("owned: %+v", s.owned(setup.KindBank, "abuja-digital"))
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
	ob.get("/login")
	ob.post("/login", url.Values{"username": {p.Username}, "password": {p.Password}})
	if !strings.Contains(ob.last, "The username or password is wrong.") {
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
		{"name": "Bank E", "type": "credit-union"},
		{"name": "Bank F", "nip_code": "99903"},
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
	s := newBankStack(t)
	conn := s.connect("chidi", "bank-b", s.today().AddDays(-10))
	chidi, tunde := s.persona("chidi").Accounts[0], s.persona("tunde").Accounts[0]
	st := s.ctl("POST", "/banks/bank-b/suspend", nil)
	if st["phase"] != connect.PhaseSuspended {
		t.Fatalf("suspend: %v", st)
	}
	if _, err := s.payTry("bank-b", chidi, "bank-a", tunde, "1000", nil); err == nil || !strings.Contains(err.Error(), "bank_not_active") {
		t.Fatalf("transfer out of a suspended bank: %v", err)
	}
	if _, err := s.payTry("bank-a", tunde, "bank-b", chidi, "1000", nil); err == nil || !strings.Contains(err.Error(), "nothing was debited") {
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
	cfg, err := connect.LoadConfig(s.cfg.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Bank("bank-b").Suspended() {
		t.Fatal("suspension not saved")
	}
	s.ctl("POST", "/banks/bank-b/resume", nil)
	s.pay("bank-a", tunde, "bank-b", chidi, "1000")
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
	s := newBankStack(t)
	conn := s.connect("chidi", "bank-b", s.today().AddDays(-10))
	acct := fmt.Sprint(conn.Accounts[0]["id"])
	p := s.persona("chidi")
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
	dropped := s.realNow()
	if _, err := db.Exec(context.Background(), "DROP DATABASE "+s.cfg.Bank("bank-b").Database+" WITH (FORCE)"); err != nil {
		t.Fatal(err)
	}
	s.advance("24h")
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
	checkProvisioned(t, storedBank(t, s.cfg.Dir, "bank-b"))
	s.syncNow()
	if r := s.call("GET", "/v1/accounts/"+acct+"/balance", nil); r.Status != 409 || r.Code() != "reauth_required" {
		t.Fatalf("consents live in the bank database, so a rebuilt bank should ask for consent again: %d %s", r.Status, r.Body)
	}
	b := s.bankBrowser()
	b.base = s.cfg.Bank("bank-b").URL()
	b.get("/login")
	b.post("/login", url.Values{"username": {p.Username}, "password": {p.Password}})
	if !strings.Contains(b.last, "The username or password is wrong.") {
		t.Fatalf("a rebuilt bank starts empty, so old sign-ins fail: %.300s", b.last)
	}
	if _, err := s.applyWithout("bank-b", map[string]any{"prune": true, "money": map[string]any{"money": "transfer", "to": "bank-a", "customers": map[string]string{"cus_nobody": "leave"}}}); err != nil {
		t.Fatal(err)
	}
	s.waitBlocked("bank-b", setup.ReasonMoneyDecisionNeeded, "cus_nobody is not a customer of Bank B")
	dropped = s.realNow()
	if _, err := db.Exec(context.Background(), "DROP DATABASE "+s.cfg.Bank("bank-b").Database+" WITH (FORCE)"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(60 * time.Second)
	for {
		st := s.waitPhase("bank-b", connect.PhaseTerminating, 30*time.Second)
		if ne := s.ctl("GET", "/nip/name-enquiry?bank_code="+s.cfg.Bank("bank-b").NIPCode+"&account_number="+p.Accounts[0], nil); ne["response_code"] != "91" {
			t.Fatalf("name enquiry at a terminating bank that is being repaired: %v", ne)
		}
		rebuilt, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(st["steps"].([]any)[2].(map[string]any)["at"]))
		if rebuilt.After(dropped) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("bank-b was not rebuilt while terminating: %v", st)
		}
		time.Sleep(200 * time.Millisecond)
	}
	checkSettled(t, s.app)
}

func TestOpenAccountAtNewBank(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newBankStack(t)
	s.ctl("POST", "/banks", map[string]any{"name": "Abuja Digital", "type": "digital"})
	s.waitPhase("abuja-digital", connect.PhaseActive, 60*time.Second)
	p := s.join("adaeze", "abuja-digital")
	if p.BVN != s.persona("adaeze").BVN {
		t.Fatalf("a second bank gave adaeze BVN %s, want %s", p.BVN, s.persona("adaeze").BVN)
	}
	nuban := s.openAccount(p, "current")
	if nuban != world.NUBAN(s.cfg.Bank("abuja-digital").NUBANCode, 1) {
		t.Fatalf("new account number %q", nuban)
	}
	s.pay("bank-a", s.persona("adaeze").Accounts[0], "abuja-digital", nuban, "25000")
	b := s.bankBrowser()
	b.base = s.cfg.Bank("abuja-digital").URL()
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

func TestCloseImpact(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newBankStack(t)
	s.ctl("POST", "/banks", map[string]any{"name": "Abuja Digital", "type": "digital"})
	s.waitPhase("abuja-digital", connect.PhaseActive, 60*time.Second)
	empty := s.ctl("GET", "/banks/abuja-digital/close", nil)
	if len(empty["customers"].([]any)) != 0 || empty["deposits_minor"] != float64(0) {
		t.Fatalf("new bank impact: %v", empty)
	}
	p := s.join("adaeze", "abuja-digital")
	s.pay("bank-a", s.persona("adaeze").Accounts[0], "abuja-digital", s.openAccount(p, "current"), "25000")
	s.connect("adaeze", "abuja-digital", s.today().AddDays(-3))
	imp := s.ctl("GET", "/banks/abuja-digital/close", nil)
	if imp["insured_limit_minor"] != float64(500_000_000) || len(imp["blockers"].([]any)) != 0 {
		t.Fatalf("impact: %v", imp)
	}
	customers := imp["customers"].([]any)
	if len(customers) != 1 || customers[0].(map[string]any)["customer"] != p.Customer || customers[0].(map[string]any)["deposits_minor"] != float64(2_500_000) {
		t.Fatalf("customers: %v", customers)
	}
	accounts := imp["accounts"].([]any)
	if len(accounts) != 1 || accounts[0].(map[string]any)["connected"] != true {
		t.Fatalf("accounts: %v", accounts)
	}
	if conns := imp["connections"].([]any); len(conns) != 1 || conns[0].(map[string]any)["partner"] != "balance" {
		t.Fatalf("connections: %v", conns)
	}
	if st := s.ctl("GET", "/banks", nil); !strings.Contains(fmt.Sprint(st["data"]), "abuja-digital") {
		t.Fatalf("banks: %v", st)
	}
	s.waitPhase("abuja-digital", connect.PhaseActive, time.Second)
	big := s.ctl("GET", "/banks/bank-a/close", nil)
	if big["insured_minor"].(float64)+big["uninsured_minor"].(float64) != big["deposits_minor"].(float64) || len(big["customers"].([]any)) == 0 {
		t.Fatalf("bank-a impact: %v", big)
	}
	if _, err := s.ctlTry("GET", "/banks/nope/close", nil); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("unknown bank: %v", err)
	}
}

func TestCloseBank(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newBankStack(t)
	s.ctl("POST", "/banks", map[string]any{"name": "Abuja Digital", "type": "digital"})
	s.waitPhase("abuja-digital", connect.PhaseActive, 60*time.Second)
	fund := map[string]string{"adaeze": "25000", "tunde": "10000"}
	home, opened := map[string]persona{}, map[string]persona{}
	for _, id := range []string{"adaeze", "tunde"} {
		home[id] = s.persona(id)
		opened[id] = s.join(id, "abuja-digital")
		s.pay("bank-a", home[id].Accounts[0], "abuja-digital", s.openAccount(opened[id], "current"), fund[id])
		opened[id] = s.personaAt(id, "abuja-digital")
	}
	conn := s.connect("adaeze", "abuja-digital", s.today().AddDays(-3))
	acct := fmt.Sprint(conn.Accounts[0]["id"])
	balance := func(p persona) int64 {
		return s.balances(p)[p.Accounts[0]]
	}
	before := balance(home["adaeze"])
	for _, bad := range []map[string]any{{"money": "transfer"}, {"money": "transfer", "to": "abuja-digital"}, {"money": "keep", "to": "bank-a"}} {
		if _, err := s.ctlTry("POST", "/banks/abuja-digital/close", bad); err == nil || !strings.Contains(err.Error(), "HTTP 422") {
			t.Fatalf("close with %v: %v", bad, err)
		}
	}
	for _, bad := range []map[string]any{{"kind": "timeout", "bank": "bank-a", "outcome": "failed", "count": 0}, {"kind": "timeout", "bank": "bank-a", "outcome": "failed", "count": -2}, {"kind": "down", "bank": "bank-a", "count": 1}} {
		if _, err := s.ctlTry("POST", "/faults", bad); err == nil || !strings.Contains(err.Error(), "HTTP 422") {
			t.Fatalf("fault %v: %v", bad, err)
		}
	}
	if _, err := s.ctlTry("POST", "/faults", map[string]any{"kind": "timeout", "bank": "bank-a", "outcome": "failed", "count": 0}); err == nil || !strings.Contains(err.Error(), "count 0 must be 1 or more") {
		t.Fatalf("fault count 0 message: %v", err)
	}
	if got := s.ctl("POST", "/faults", map[string]any{"kind": "timeout", "bank": "bank-a", "outcome": "failed"}); got["left"] != float64(1) {
		t.Fatalf("fault without a count: %v", got)
	}
	snap := s.ctl("POST", "/snapshots", map[string]string{"name": "before the close"})
	accepted := s.ctl("POST", "/banks/abuja-digital/close", map[string]any{"money": "transfer", "to": "bank-a", "customers": map[string]string{opened["tunde"].Customer: "leave"}})
	if accepted["phase"] != setup.PhaseClosing || accepted["deletion_requested_at"] == nil {
		t.Fatalf("close was not admitted as closing: %v", accepted)
	}
	s.waitBlocked("abuja-digital", setup.ReasonSettlementPending, "to settle 2 transfers (net ₦35,000.00). Wait for it, or run beerus nip settle-now.")
	if ne := s.ctl("GET", "/nip/name-enquiry?bank_code="+s.cfg.Bank("abuja-digital").NIPCode+"&account_number="+opened["tunde"].Accounts[0], nil); ne["response_code"] != "91" {
		t.Fatalf("name enquiry at a terminating bank: %v", ne)
	}
	if got := s.eventsOf("connection.status_changed", func(d map[string]any) bool {
		return d["connection_id"] == conn.ConnectionID && d["status"] == "revoked"
	}); len(got) != 1 {
		t.Fatalf("connections must be revoked before the money moves: %v", got)
	}
	s.ctl("POST", "/nip/settle-now", nil)
	s.ctl("POST", "/nip/settle-now", nil)
	s.waitBlocked("abuja-digital", setup.ReasonTargetUnavailable, "Bank A has a fault")
	if got := balance(home["adaeze"]); got != before {
		t.Fatalf("money moved while the destination had a fault: %d, want %d", got, before)
	}
	if _, err := s.ctlTry("POST", "/snapshots/"+fmt.Sprint(snap["id"])+"/restore", nil); err == nil || !strings.Contains(err.Error(), "HTTP 409") || !strings.Contains(err.Error(), "Abuja Digital is closing, so a snapshot cannot be restored until that finishes") {
		t.Fatalf("restore while a bank is closing: %v", err)
	}
	if got := s.ctl("DELETE", "/faults?bank=bank-a", nil); got["cleared"] != float64(1) {
		t.Fatalf("clear faults: %v", got)
	}
	s.ctl("POST", "/banks/bank-a/suspend", nil)
	s.waitBlocked("abuja-digital", setup.ReasonTargetUnavailable, "Bank A is suspended or down, so money cannot move there. Resume it or choose another bank.")
	if got := balance(home["adaeze"]); got != before {
		t.Fatalf("money moved while the destination was suspended: %d, want %d", got, before)
	}
	s.ctl("POST", "/banks/bank-a/resume", nil)
	s.waitBlocked("abuja-digital", setup.ReasonSettlementPending, "to settle 1 transfer (net ₦25,000.00)")
	s.ctl("POST", "/nip/settle-now", nil)
	res := s.closeResult("abuja-digital")
	if res["moved_minor"] != float64(2_500_000) || res["left_minor"] != float64(1_000_000) || len(res["revoked_connections"].([]any)) != 1 {
		t.Fatalf("close: %v", res)
	}
	wantLines := fmt.Sprint([]any{
		map[string]any{"customer": opened["adaeze"].Customer, "name": res["lines"].([]any)[0].(map[string]any)["name"], "nuban": opened["adaeze"].Accounts[0], "outcome": "transferred", "amount_minor": float64(2_500_000), "to_bank": "bank-a", "to_bank_name": "Bank A", "to_nuban": home["adaeze"].Accounts[0]},
		map[string]any{"customer": opened["tunde"].Customer, "name": res["lines"].([]any)[1].(map[string]any)["name"], "nuban": opened["tunde"].Accounts[0], "outcome": "left", "amount_minor": float64(1_000_000)},
	})
	if got := fmt.Sprint(res["lines"]); got != wantLines {
		t.Fatalf("close lines:\n%s\nwant\n%s", got, wantLines)
	}
	if kept := s.ctl("GET", "/closures/abuja-digital", nil); fmt.Sprint(kept["lines"]) != wantLines || kept["name"] != "Abuja Digital" {
		t.Fatalf("kept closure: %v", kept)
	}
	if got := balance(home["adaeze"]); got != before+2_500_000 {
		t.Fatalf("adaeze at bank-a has %d, want %d", got, before+2_500_000)
	}
	s.advance("72h")
	if got := balance(opened["adaeze"]); got != 0 {
		t.Fatalf("closed account keeps %d", got)
	}
	if got := balance(opened["tunde"]); got != 1_000_000 {
		t.Fatalf("left account has %d", got)
	}
	if c, err := s.app.VerifyBank(); err != nil || !c.OK() {
		t.Fatalf("bank ledgers after the close: %v %v", err, c)
	}
	if got := s.eventsOf("connection.status_changed", func(d map[string]any) bool {
		return d["connection_id"] == conn.ConnectionID && d["status"] == "revoked"
	}); len(got) != 1 {
		t.Fatalf("revoked webhooks: %v", got)
	}
	if got := s.eventsOf("account.removed", func(d map[string]any) bool { return d["account_id"] == acct }); len(got) != 1 {
		t.Fatalf("account.removed webhooks: %v", got)
	}
	s.must(s.call("GET", "/v1/accounts/"+acct+"/balance", nil), 409)
	if _, err := http.Get(s.cfg.Bank("abuja-digital").URL() + "/login"); err == nil {
		t.Fatal("online banking still answers")
	}
	for _, path := range []string{"/banks/abuja-digital/resume", "/banks/abuja-digital/suspend", "/banks/abuja-digital/close"} {
		if _, err := s.ctlTry("POST", path, map[string]any{"to": "bank-a"}); err == nil || !strings.Contains(err.Error(), "HTTP 422") {
			t.Fatalf("%s on a closed bank: %v", path, err)
		}
	}
	if _, err := s.payTry("bank-a", home["tunde"].Accounts[0], "abuja-digital", opened["tunde"].Accounts[0], "100", nil); err == nil || !strings.Contains(err.Error(), "bank_closed") {
		t.Fatalf("transfer into a closed bank: %v", err)
	}
	if _, err := s.ctlTry("POST", "/banks/abuja-digital/accounts", map[string]any{"customer": opened["tunde"].Customer, "type": "current"}); err == nil || !strings.Contains(err.Error(), "HTTP 409") {
		t.Fatalf("open account at a closed bank: %v", err)
	}
	s.waitPhase("abuja-digital", connect.PhaseClosed, time.Second)
	if err := s.app.Save(); err != nil {
		t.Fatal(err)
	}
	saved := s.loadConfig()
	if !saved.Bank("abuja-digital").Closed() {
		t.Fatal("the closed state was not saved")
	}
	again, err := connect.New(saved, connect.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range again.BankStatuses() {
		if st.ID == "abuja-digital" && st.Phase != connect.PhaseClosed {
			t.Fatalf("after restart: %s", st.Phase)
		}
	}
	if got := balance(home["adaeze"]); got != before+2_500_000 {
		t.Fatalf("money moved again after restart: %d, want %d", got, before+2_500_000)
	}
	checkSettled(t, s.app)
}

func TestClosePaysInsuredDeposits(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newBankStack(t)
	s.ctl("POST", "/banks", map[string]any{"name": "Kano Microfinance", "type": "microfinance"})
	s.waitPhase("kano-microfinance", connect.PhaseActive, 60*time.Second)
	if imp := s.ctl("GET", "/banks/bank-a/close", nil); !strings.Contains(fmt.Sprint(imp["blockers"]), "Kano Microfinance settles through this bank") {
		t.Fatalf("bank-a blockers: %v", imp["blockers"])
	}
	from := s.persona("tunde")
	who := s.join("tunde", "kano-microfinance")
	for _, h := range []opening{{"current", "1500000"}, {"savings", "1000000"}} {
		s.pay("bank-a", from.Accounts[0], "kano-microfinance", s.openAccount(who, h.typ), h.naira)
	}
	total := func(p persona) int64 {
		var sum int64
		for _, v := range s.balances(p) {
			sum += v
		}
		return sum
	}
	before := total(from)
	s.ctl("POST", "/banks/kano-microfinance/close", map[string]any{"money": "payout", "to": "bank-a"})
	res := s.closeResult("kano-microfinance")
	if res["paid_out_minor"] != float64(200_000_000) || res["claims_minor"] != float64(50_000_000) || res["moved_minor"] != float64(0) {
		t.Fatalf("close: %v", res)
	}
	outcomes := map[string]float64{}
	for _, l := range res["lines"].([]any) {
		line := l.(map[string]any)
		outcomes[fmt.Sprint(line["outcome"])] += line["amount_minor"].(float64)
		if line["customer"] != who.Customer || line["name"] == "" || line["to_bank"] != nil {
			t.Fatalf("payout line: %v", line)
		}
	}
	if fmt.Sprint(outcomes) != "map[claim:5e+07 paid_out:2e+08]" {
		t.Fatalf("payout outcomes: %v", outcomes)
	}
	if kept := s.ctl("GET", "/closures/kano-microfinance", nil); fmt.Sprint(kept["lines"]) != fmt.Sprint(res["lines"]) {
		t.Fatalf("kept closure: %v", kept)
	}
	if got := total(from); got != before {
		t.Fatalf("a cash payout moved %d to bank-a", got-before)
	}
	s.advance("72h")
	if got := total(who); got != 50_000_000 {
		t.Fatalf("claims left at the closed bank: %d", got)
	}
	for _, nuban := range who.Accounts {
		if _, err := s.payTry("kano-microfinance", nuban, "bank-a", from.Accounts[0], "1", nil); err == nil {
			t.Fatalf("account %s can still send after the payout", nuban)
		}
	}
	if imp := s.ctl("GET", "/banks/bank-a/close", nil); len(imp["blockers"].([]any)) != 0 {
		t.Fatalf("bank-a still blocked after the microfinance bank closed: %v", imp["blockers"])
	}
	checkSettled(t, s.app)
}

func TestRestoreCannotReopenAClosedBank(t *testing.T) {
	s := newStack(t)
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	snap := s.ctl("POST", "/snapshots", map[string]string{"name": "before"})
	s.ctl("POST", "/banks/bank-b/close", map[string]any{"money": "leave"})
	s.waitPhase("bank-b", connect.PhaseClosed, 60*time.Second)
	_, err := s.ctlTry("POST", "/snapshots/"+fmt.Sprint(snap["id"])+"/restore", nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 409") || !strings.Contains(err.Error(), "cannot be undone") {
		t.Fatalf("restore before the close: %v", err)
	}
	ids := func(state string) []string {
		out := []string{}
		for _, d := range s.ctl("GET", "/banks?state="+state, nil)["data"].([]any) {
			out = append(out, fmt.Sprint(d.(map[string]any)["id"]))
		}
		return out
	}
	if got := fmt.Sprint(ids("closed")); got != "[bank-b]" {
		t.Fatalf("closed banks: %s", got)
	}
	if got := fmt.Sprint(ids("active")); got != "[bank-a]" {
		t.Fatalf("active banks: %s", got)
	}
	if got := len(s.ctl("GET", "/banks", nil)["data"].([]any)); got != 2 {
		t.Fatalf("all banks: %d", got)
	}
}
