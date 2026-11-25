package contracttest

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/core"
	"github.com/evolutionlabs-core/beerus/internal/postgres"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

func (s *Stack) waitPhase(id, phase string) map[string]any {
	s.t.Helper()
	var last map[string]any
	waitFor(s.t, waitLimit, "bank "+id+" to reach "+phase, func() bool {
		for _, d := range s.ctl("GET", "/banks", nil)["data"].([]any) {
			if st := d.(map[string]any); st["id"] == id {
				last = st
				return st["phase"] == phase
			}
		}
		return false
	})
	return last
}

func (s *Stack) spare(id string) string {
	s.t.Helper()
	s.waitPhase(id, core.PhaseActive)
	return id
}

func (s *Stack) waitResource(kind, id string, done func(map[string]any) bool) map[string]any {
	s.t.Helper()
	var last map[string]any
	defer func() {
		if s.t.Failed() {
			s.t.Logf("%s/%s ended as %v", kind, id, last)
		}
	}()
	waitFor(s.t, waitLimit, kind+"/"+id+" to get there", func() bool {
		r, err := s.ctlTry("GET", "/resources/"+kind+"/"+id, nil)
		switch {
		case err == nil:
			last = r
			return done(r)
		case strings.Contains(err.Error(), "HTTP 404"):
			return done(nil)
		}
		s.t.Fatal(err)
		return false
	})
	return last
}

func (s *Stack) waitBlocked(id, reason, text string) {
	s.t.Helper()
	s.waitResource("bank", id, func(r map[string]any) bool {
		return slices.ContainsFunc(asList(r["conditions"]), func(c map[string]any) bool {
			return c["type"] == setup.ConditionBlocked && c["status"] == "True" && c["reason"] == reason && strings.Contains(fmt.Sprint(c["message"]), text)
		})
	})
}

func (s *Stack) waitGone(kind, id string) {
	s.t.Helper()
	s.waitResource(kind, id, func(r map[string]any) bool { return r == nil })
}

func (s *Stack) settleUntil(id, phase string) {
	s.t.Helper()
	waitFor(s.t, waitLimit, "bank "+id+" to reach "+phase, func() bool {
		s.ctl("POST", "/nip/settle-now", nil)
		return slices.ContainsFunc(asList(s.ctl("GET", "/banks", nil)["data"]), func(b map[string]any) bool { return b["id"] == id && b["phase"] == phase })
	})
}

func (s *Stack) closeResult(id string) map[string]any {
	s.t.Helper()
	s.settleUntil(id, core.PhaseClosed)
	res, ok := s.ctl("GET", "/resources/bank/"+id, nil)["result"].(map[string]any)
	if !ok {
		s.t.Fatalf("bank %s has no close result", id)
	}
	return res
}

func (s *Stack) applyWithout(id string, extra map[string]any) (map[string]any, error) {
	s.t.Helper()
	st, err := setup.LoadStatus(s.cfg.Dir)
	if err != nil {
		s.t.Fatal(err)
	}
	spec := st.Applied
	spec.Spec.Banks = slices.DeleteFunc(spec.Spec.Banks, func(b setup.Bank) bool { return b.ID == id })
	raw, err := setup.Export(spec)
	if err != nil {
		s.t.Fatal(err)
	}
	body := map[string]any{"file": "setup.yaml", "spec": string(raw)}
	maps.Copy(body, extra)
	return s.ctlTry("POST", "/apply", body)
}

func (s *Stack) funded(bank, who, naira string) (home, opened persona) {
	s.t.Helper()
	home = s.persona(who)
	opened = s.join(who, bank)
	s.pay("bank-a", home.Accounts[0], bank, s.openAccount(opened, "current"), naira)
	return home, s.people[s.key(who, bank)]
}

func (s *Stack) loginAt(bankID string, p persona) *bankBrowser {
	s.t.Helper()
	b := s.bankBrowserAt(bankID)
	b.get("/login")
	b.post("/login", url.Values{"username": {p.Username}, "password": {p.Password}})
	return b
}

func TestBankLifecycle(t *testing.T) {
	s := sharedStack(t, 0)
	s.needsNodes()

	s.run("create and open an account", func(s *Stack) {
		p := s.persona("adaeze")
		out := s.ctl("POST", "/banks", map[string]any{"name": "Abuja Digital", "type": "digital"})
		if out["id"] != "abuja-digital" || out["phase"] != core.PhaseProvisioning {
			t.Fatalf("create: %v", out)
		}
		st := s.waitPhase("abuja-digital", core.PhaseActive)
		for _, step := range st["steps"].([]any) {
			if step.(map[string]any)["done"] != true || strings.HasPrefix(fmt.Sprint(step.(map[string]any)["at"]), "0001-") {
				t.Fatalf("step not done in real time: %v", step)
			}
		}
		b := s.loadConfig().Bank("abuja-digital")
		if b == nil || b.Type != "digital" || b.NUBANCode == "" || b.Database != config.DatabaseName(s.cfg.Dir, "abuja-digital") {
			t.Fatalf("saved bank: %+v", b)
		}
		claimed := map[string]bool{}
		for _, o := range s.owned(setup.KindBank, "abuja-digital") {
			claimed[o.Kind] = true
		}
		if !claimed["database"] || !claimed["port"] {
			t.Fatalf("owned: %+v", s.owned(setup.KindBank, "abuja-digital"))
		}
		if resp, err := s.raw.Get(b.URL() + "/login"); err != nil || resp.StatusCode != 200 {
			t.Fatalf("online banking: %v %v", resp, err)
		} else {
			resp.Body.Close()
		}
		if ob := s.loginAt("abuja-digital", p); !strings.Contains(ob.last, "The username or password is wrong.") {
			t.Fatalf("sign-in at a bank with no customers: %.300s", ob.last)
		}
		if br := s.browser(); func() bool { br.get(s.linkURL("w", "")); return !strings.Contains(br.last, "Abuja Digital") }() {
			t.Error("the widget does not list the new bank")
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
		who := s.join("adaeze", "abuja-digital")
		if who.BVN != p.BVN {
			t.Fatalf("a second bank gave adaeze BVN %s, want %s", who.BVN, p.BVN)
		}
		nuban := s.openAccount(who, "current")
		if nuban != world.NUBAN(b.NUBANCode, 1) {
			t.Fatalf("new account number %q", nuban)
		}
		s.payListed("bank-a", p.Accounts[0], "abuja-digital", nuban, "25000")
		in := s.signInAt("abuja-digital", "adaeze")
		if !strings.Contains(in.last, nuban) || !strings.Contains(in.last, "25,000.00") {
			t.Fatalf("online banking does not show the funded account: %.600s", in.last)
		}
		if conn := s.connect("adaeze", "abuja-digital"); len(conn.Accounts) != 1 || conn.Accounts[0]["account_number"] != nuban {
			t.Fatalf("connected accounts: %v", conn.Accounts)
		}
	})

	s.run("suspend and resume", func(s *Stack) {
		conn := s.connect("chidi", "bank-b")
		chidi, tunde := s.persona("chidi").Accounts[0], s.persona("tunde").Accounts[0]
		if st := s.ctl("POST", "/banks/bank-b/suspend", nil); st["phase"] != core.PhaseSuspended {
			t.Fatalf("suspend: %v", st)
		}
		t.Cleanup(func() { s.ctl("POST", "/banks/bank-b/resume", nil) })
		if _, err := s.payTry("bank-b", chidi, "bank-a", tunde, "1000", nil); err == nil || !strings.Contains(err.Error(), "bank_not_active") {
			t.Errorf("transfer out of a suspended bank: %v", err)
		}
		if _, err := s.payTry("bank-a", tunde, "bank-b", chidi, "1000", nil); err == nil || !strings.Contains(err.Error(), "nothing was debited") {
			t.Errorf("transfer into a suspended bank: %v", err)
		}
		s.syncNow()
		if got := s.status(conn.AccessToken); got != "institution_down" {
			t.Errorf("status while suspended: %v", got)
		}
		if resp, err := s.raw.Get(s.cfg.Bank("bank-b").URL() + "/login"); err != nil || resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("online banking while suspended: %v %v", resp, err)
		} else {
			resp.Body.Close()
		}
		if !s.loadConfig().Bank("bank-b").Suspended() {
			t.Error("suspension not saved")
		}
		s.ctl("POST", "/banks/bank-b/resume", nil)
		s.pay("bank-a", tunde, "bank-b", chidi, "1000")
		s.syncNow()
		if got := s.status(conn.AccessToken); got != "active" {
			t.Errorf("status after resume: %v", got)
		}
		if _, err := s.ctlTry("POST", "/banks/nowhere/suspend", nil); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
			t.Errorf("unknown bank: %v", err)
		}
	})

	s.run("a dropped database is rebuilt", func(s *Stack) {
		id := s.spare("repair-digital")
		who := s.join("chidi", id)
		nuban := s.openAccount(who, "current")
		conn := s.connect("chidi", id)
		acct := fmt.Sprint(conn.Accounts[0]["id"])
		pc, err := pgx.ParseConfig(s.cfg.DatabaseURL)
		if err != nil {
			t.Fatal(err)
		}
		pc.Database = "postgres"
		db, err := pgx.ConnectConfig(t.Context(), pc)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close(context.WithoutCancel(t.Context()))
		drop := func(phase string) {
			t.Helper()
			dropped := time.Now()
			if _, err := db.Exec(t.Context(), "DROP DATABASE "+s.cfg.Bank(id).Database+" WITH (FORCE)"); err != nil {
				t.Fatal(err)
			}
			waitFor(t, waitLimit, "the "+phase+" bank to be rebuilt", func() bool {
				st := s.waitPhase(id, phase)
				if phase == core.PhaseTerminating {
					if ne := s.ctl("GET", "/nip/name-enquiry?bank_code="+s.cfg.Bank(id).NIPCode+"&account_number="+nuban, nil); ne["response_code"] != "91" {
						t.Fatalf("name enquiry at a terminating bank that is being repaired: %v", ne)
					}
				}
				steps, _ := st["steps"].([]any)
				if len(steps) < 3 {
					return false
				}
				rebuilt, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(steps[2].(map[string]any)["at"]))
				return rebuilt.After(dropped)
			})
		}
		drop(core.PhaseActive)
		checkProvisioned(t, activeBank(t, s.cfg.Dir, id))
		s.syncNow()
		if r := s.call("GET", "/v1/accounts/"+acct+"/balance", nil, withToken(conn.AccessToken)); r.Status != 409 || r.Code() != "reauth_required" {
			t.Errorf("consents live in the bank database, so a rebuilt bank should ask for consent again: %d %s", r.Status, r.Body)
		}
		if b := s.loginAt(id, who); !strings.Contains(b.last, "The username or password is wrong.") {
			t.Errorf("a rebuilt bank starts empty, so old sign-ins fail: %.300s", b.last)
		}
		if _, err := s.applyWithout(id, map[string]any{"prune": true, "money": map[string]any{"money": "transfer", "to": "bank-a", "customers": map[string]string{"cus_nobody": "leave"}}}); err != nil {
			t.Fatal(err)
		}
		s.waitBlocked(id, setup.ReasonMoneyDecisionNeeded, "cus_nobody is not a customer of Repair Digital")
		drop(core.PhaseTerminating)
		if _, err := s.applyWithout(id, map[string]any{"prune": true, "money": map[string]any{"money": "leave"}}); err != nil {
			t.Fatal(err)
		}
		s.waitGone("bank", id)
		checkSettled(t, s.app)
	})
}

func TestBankClose(t *testing.T) {
	s := sharedStack(t, 0)
	s.needsNodes()

	s.run("impact, close and a second close", func(s *Stack) {
		id := s.spare("close-digital")
		empty := s.ctl("GET", "/banks/"+id+"/close", nil)
		if len(empty["customers"].([]any)) != 0 || empty["deposits_minor"] != float64(0) {
			t.Fatalf("new bank impact: %v", empty)
		}
		home, opened := map[string]persona{}, map[string]persona{}
		for _, f := range []struct{ who, naira string }{{"adaeze", "25000"}, {"tunde", "10000"}} {
			id2 := f.who
			home[id2], opened[id2] = s.funded(id, id2, f.naira)
		}
		conn := s.connect("adaeze", id)
		acct := fmt.Sprint(conn.Accounts[0]["id"])
		imp := s.ctl("GET", "/banks/"+id+"/close", nil)
		customers := imp["customers"].([]any)
		if imp["insured_limit_minor"] != float64(500_000_000) || len(imp["blockers"].([]any)) != 0 || len(customers) != 2 || len(imp["connections"].([]any)) != 1 || imp["connections"].([]any)[0].(map[string]any)["partner"] != "balance" {
			t.Fatalf("impact: %v", imp)
		}
		if big := s.ctl("GET", "/banks/bank-a/close", nil); big["insured_minor"].(float64)+big["uninsured_minor"].(float64) != big["deposits_minor"].(float64) {
			t.Fatalf("bank-a impact: %v", big)
		}
		if _, err := s.ctlTry("GET", "/banks/nope/close", nil); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
			t.Errorf("unknown bank: %v", err)
		}
		before := s.balances(home["adaeze"])[home["adaeze"].Accounts[0]]
		for _, bad := range []map[string]any{{"money": "transfer"}, {"money": "transfer", "to": id}, {"money": "keep", "to": "bank-a"}, {"money": "payout", "to": "bank-a"}} {
			if _, err := s.ctlTry("POST", "/banks/"+id+"/close", bad); err == nil || !strings.Contains(err.Error(), "HTTP 422") {
				t.Errorf("close with %v: %v", bad, err)
			}
		}
		for _, bad := range []map[string]any{{"kind": "timeout", "bank": "bank-a", "outcome": "failed", "count": 0}, {"kind": "timeout", "bank": "bank-a", "outcome": "failed", "count": -2}, {"kind": "down", "bank": "bank-a", "count": 1}} {
			if _, err := s.ctlTry("POST", "/faults", bad); err == nil || !strings.Contains(err.Error(), "HTTP 422") {
				t.Errorf("fault %v: %v", bad, err)
			}
		}
		if got := s.ctl("POST", "/faults", map[string]any{"kind": "timeout", "bank": "bank-a", "outcome": "failed"}); got["left"] != float64(1) {
			t.Fatalf("fault without a count: %v", got)
		}
		accepted := s.ctl("POST", "/banks/"+id+"/close", map[string]any{"money": "transfer", "to": "bank-a", "customers": map[string]string{opened["tunde"].Customer: "leave"}})
		if accepted["phase"] != config.PhaseClosing || accepted["deletion_requested_at"] == nil {
			t.Fatalf("close was not admitted as closing: %v", accepted)
		}
		s.waitBlocked(id, setup.ReasonSettlementPending, "to settle 2 transfers (net ₦35,000.00). Wait for it, or run beerus nip settle-now.")
		if got := s.eventsOf("connection.status_changed", func(d map[string]any) bool {
			return d["connection_id"] == conn.ConnectionID && d["status"] == "revoked"
		}); len(got) != 1 {
			t.Errorf("connections must be revoked before the money moves: %v", got)
		}
		s.ctl("POST", "/nip/settle-now", nil)
		s.ctl("POST", "/nip/settle-now", nil)
		s.waitBlocked(id, setup.ReasonTargetUnavailable, "Bank A has a fault")
		s.ctl("POST", "/banks/bank-a/suspend", nil)
		t.Cleanup(func() { s.ctl("POST", "/banks/bank-a/resume", nil) })
		if got := s.ctl("DELETE", "/faults?bank=bank-a", nil); got["cleared"] != float64(1) {
			t.Errorf("clear faults: %v", got)
		}
		s.waitBlocked(id, setup.ReasonTargetUnavailable, "Bank A is suspended or down, so money cannot move there. Resume it or choose another bank.")
		if got := s.balances(home["adaeze"])[home["adaeze"].Accounts[0]]; got != before {
			t.Errorf("money moved while the destination was unavailable: %d, want %d", got, before)
		}
		s.ctl("POST", "/banks/bank-a/resume", nil)
		s.waitBlocked(id, setup.ReasonSettlementPending, "to settle 1 transfer (net ₦25,000.00)")
		s.ctl("POST", "/nip/settle-now", nil)
		res := s.closeResult(id)
		if res["moved_minor"] != float64(2_500_000) || res["left_minor"] != float64(1_000_000) || len(res["revoked_connections"].([]any)) != 1 {
			t.Fatalf("close: %v", res)
		}
		lines := res["lines"].([]any)
		if l := lines[0].(map[string]any); l["customer"] != opened["adaeze"].Customer || l["outcome"] != "transferred" || l["to_bank"] != "bank-a" || l["to_nuban"] != home["adaeze"].Accounts[0] || l["amount_minor"] != float64(2_500_000) {
			t.Errorf("transferred line: %v", l)
		}
		if l := lines[1].(map[string]any); l["customer"] != opened["tunde"].Customer || l["outcome"] != "left" || l["amount_minor"] != float64(1_000_000) {
			t.Errorf("left line: %v", l)
		}
		if kept := s.ctl("GET", "/closures/"+id, nil); fmt.Sprint(kept["lines"]) != fmt.Sprint(lines) || kept["name"] != "Close Digital" {
			t.Errorf("kept closure: %v", kept)
		}
		if got := s.balances(home["adaeze"])[home["adaeze"].Accounts[0]]; got != before+2_500_000 {
			t.Errorf("adaeze at bank-a has %d, want %d", got, before+2_500_000)
		}
		s.syncNow()
		if got := s.closedBalances(opened["adaeze"])[opened["adaeze"].Accounts[0]]; got != 0 {
			t.Errorf("closed account keeps %d", got)
		}
		if got := s.closedBalances(opened["tunde"])[opened["tunde"].Accounts[0]]; got != 1_000_000 {
			t.Errorf("left account has %d", got)
		}
		if got := s.eventsOf("account.removed", func(d map[string]any) bool { return d["account_id"] == acct }); len(got) != 1 {
			t.Errorf("account.removed webhooks: %v", got)
		}
		s.must(s.call("GET", "/v1/accounts/"+acct+"/balance", nil, withToken(conn.AccessToken)), 409)
		waitFor(t, waitLimit, "online banking to stop answering", func() bool {
			resp, err := s.raw.Get(s.cfg.Bank(id).URL() + "/login")
			if err == nil {
				resp.Body.Close()
			}
			return err != nil
		})
		for _, path := range []string{"/resume", "/suspend", "/close"} {
			if _, err := s.ctlTry("POST", "/banks/"+id+path, map[string]any{"to": "bank-a"}); err == nil || !strings.Contains(err.Error(), "HTTP 422") {
				t.Errorf("%s on a closed bank: %v", path, err)
			}
		}
		if got := newCLIAt(t, s.cfg.Dir).fails("bank", "close", id, "--money", "leave"); got != "beerus: "+id+" is already closed. Nothing was changed" {
			t.Errorf("close twice: %q", got)
		}
		if _, err := s.payTry("bank-a", home["tunde"].Accounts[0], id, opened["tunde"].Accounts[0], "100", nil); err == nil || !strings.Contains(err.Error(), `"code":"bank_unavailable","message":"Close Digital is not available right now, so nothing was debited."`) {
			t.Errorf("transfer into a closed bank: %v", err)
		}
		if _, err := s.ctlTry("POST", "/banks/"+id+"/accounts", map[string]any{"customer": opened["tunde"].Customer, "type": "current"}); err == nil || !strings.Contains(err.Error(), "HTTP 409") {
			t.Errorf("open account at a closed bank: %v", err)
		}
		if closed := asList(s.ctl("GET", "/banks?state=closed", nil)["data"]); !slices.ContainsFunc(closed, func(b map[string]any) bool { return b["id"] == id }) {
			t.Errorf("closed banks: %v", closed)
		}
		if active := fmt.Sprint(s.ctl("GET", "/banks?state=active", nil)["data"]); strings.Contains(active, id) {
			t.Errorf("active banks list a closed bank: %v", active)
		}
	})

	s.run("a microfinance bank pays insured deposits", func(s *Stack) {
		id := s.spare("kano-microfinance")
		if imp := s.ctl("GET", "/banks/bank-a/close", nil); !strings.Contains(fmt.Sprint(imp["blockers"]), "Kano Microfinance settles through this bank") {
			t.Fatalf("bank-a blockers: %v", imp["blockers"])
		}
		from := s.persona("tunde")
		who := s.join("tunde", id)
		for _, h := range []opening{{"current", "1500000"}, {"savings", "1000000"}} {
			s.pay("bank-a", from.Accounts[0], id, s.openAccount(who, h.typ), h.naira)
		}
		total := func(balances func(persona) map[string]int64, p persona) int64 {
			var sum int64
			for _, v := range balances(p) {
				sum += v
			}
			return sum
		}
		before := total(s.balances, from)
		s.ctl("POST", "/banks/"+id+"/close", map[string]any{"money": "payout"})
		res := s.closeResult(id)
		if res["paid_out_minor"] != float64(200_000_000) || res["claims_minor"] != float64(50_000_000) || res["moved_minor"] != float64(0) {
			t.Fatalf("close: %v", res)
		}
		outcomes := map[string]float64{}
		for _, l := range res["lines"].([]any) {
			line := l.(map[string]any)
			outcomes[fmt.Sprint(line["outcome"])] += line["amount_minor"].(float64)
			if line["customer"] != who.Customer || line["name"] == "" || line["to_bank"] != nil {
				t.Errorf("payout line: %v", line)
			}
		}
		if fmt.Sprint(outcomes) != "map[claim:5e+07 paid_out:2e+08]" {
			t.Errorf("payout outcomes: %v", outcomes)
		}
		if got := total(s.balances, from); got != before {
			t.Errorf("a cash payout moved %d to bank-a", got-before)
		}
		s.syncNow()
		if got := total(s.closedBalances, who); got != 50_000_000 {
			t.Errorf("claims left at the closed bank: %d", got)
		}
		for _, nuban := range who.Accounts {
			if _, err := s.payTry(id, nuban, "bank-a", from.Accounts[0], "1", nil); err == nil {
				t.Errorf("account %s can still send after the payout", nuban)
			}
		}
		if imp := s.ctl("GET", "/banks/bank-a/close", nil); len(imp["blockers"].([]any)) != 0 {
			t.Errorf("bank-a still blocked after the microfinance bank closed: %v", imp["blockers"])
		}
		checkSettled(t, s.app)
	})
}

func TestPurge(t *testing.T) {
	s := sharedStack(t, 0)
	s.needsNodes()

	s.run("nothing is left behind", func(s *Stack) {
		id := s.spare("purge-digital")
		bank := *s.cfg.Bank(id)
		home := s.persona("adaeze")
		target := s.openAccount(s.join("adaeze", id), "current")
		s.pay("bank-a", home.Accounts[0], id, target, "25000")
		conn := s.connect("adaeze", id)
		acct := fmt.Sprint(conn.Accounts[0]["id"])
		s.ctl("POST", "/faults", map[string]any{"kind": "timeout", "bank": id, "outcome": "credited", "count": 1})
		sent, _ := s.payTry("bank-a", home.Accounts[0], id, target, "100", nil)
		if sent["status"] != "pending" {
			t.Fatalf("the timeout fault did not leave the transfer pending: %v", sent)
		}
		s.ctl("POST", "/nip/sessions/"+fmt.Sprint(sent["session_id"])+"/status", nil)
		databases := func() []string {
			names, err := postgres.List(t.Context(), s.cfg.DatabaseURL, bank.Database)
			if err != nil {
				t.Fatal(err)
			}
			return names
		}
		imp := s.ctl("GET", "/banks/"+id+"/purge", nil)
		if !strings.Contains(fmt.Sprint(imp["blockers"]), "to settle 2 transfers (net ₦25,100.00)") || imp["database"] != bank.Database || imp["connections"] != float64(1) || len(databases()) != 1 {
			t.Fatalf("purge preview: %v", imp)
		}
		atBankA := s.balances(home)[home.Accounts[0]]
		if res := s.ctl("POST", "/banks/"+id+"/purge", nil); res["phase"] != config.PhaseTerminating {
			t.Fatalf("purge was not admitted as terminating: %v", res)
		}
		s.waitBlocked(id, setup.ReasonSettlementPending, "to settle 2 transfers (net ₦25,100.00). Wait for it, or run beerus nip settle-now.")
		if got := databases(); len(got) != 1 {
			t.Fatalf("the purge dropped databases while transfers were unsettled: %v", got)
		}
		if ne := s.ctl("GET", "/nip/name-enquiry?bank_code="+bank.NIPCode+"&account_number="+target, nil); ne["response_code"] != "91" {
			t.Errorf("name enquiry at a terminating bank: %v", ne)
		}
		if _, err := s.payTry("bank-a", home.Accounts[0], id, target, "100", nil); err == nil || !strings.Contains(err.Error(), "nothing was debited") {
			t.Errorf("transfer into a terminating bank: %v", err)
		}
		if _, err := s.ctlTry("POST", "/banks", map[string]any{"name": "Purge Digital", "id": id}); err == nil || !strings.Contains(err.Error(), "HTTP 409") {
			t.Errorf("re-adding a bank that is being removed: %v", err)
		}
		s.ctl("POST", "/nip/settle-now", nil)
		s.waitBlocked(id, setup.ReasonMoneyDecisionNeeded, "Purge Digital holds ₦25,100.00 for 1 customer. Choose --money transfer --to <bank>, --money payout or --money leave. Nothing moves until you do.")
		if got := databases(); len(got) != 1 {
			t.Errorf("the purge dropped databases before the money was decided: %v", got)
		}
		s.ctl("POST", "/faults", map[string]any{"kind": "down", "bank": "bank-a"})
		t.Cleanup(func() { s.ctl("DELETE", "/faults?bank=bank-a", nil) })
		if _, err := s.applyWithout(id, map[string]any{"prune": true, "money": map[string]any{"money": "transfer", "to": "bank-a"}}); err != nil {
			t.Fatal(err)
		}
		s.waitBlocked(id, setup.ReasonTargetUnavailable, "Bank A is suspended or down, so money cannot move there. Resume it or choose another bank.")
		s.ctl("DELETE", "/faults?bank=bank-a", nil)
		s.waitBlocked(id, setup.ReasonSettlementPending, "to settle 1 transfer (net ₦25,100.00)")
		if got := s.balances(home)[home.Accounts[0]]; got != atBankA+2_510_000 {
			t.Errorf("adaeze at bank-a holds %d, want %d", got, atBankA+2_510_000)
		}
		checkSettled(t, s.app)
		s.ctl("POST", "/nip/settle-now", nil)
		s.waitGone("bank", id)
		kept := s.ctl("GET", "/closures/"+id, nil)
		if lines := kept["lines"].([]any); len(lines) != 1 || lines[0].(map[string]any)["outcome"] != "transferred" || lines[0].(map[string]any)["amount_minor"] != float64(2_510_000) || lines[0].(map[string]any)["to_nuban"] != home.Accounts[0] {
			t.Errorf("closure kept after the purge: %v", kept)
		}
		if got := databases(); len(got) != 0 {
			t.Errorf("databases left behind: %v", got)
		}
		if s.loadConfig().Bank(id) != nil || s.owned(setup.KindBank, id) != nil {
			t.Error("the setup or the status store still lists the bank")
		}
		waitFor(t, waitLimit, fmt.Sprintf("port %d to be free", bank.Port), func() bool { return portOpen(bank.Port) })
		if _, err := s.ctlTry("GET", "/banks/"+id+"/purge", nil); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
			t.Errorf("preview after the purge: %v", err)
		}
		if r := s.call("GET", "/v1/accounts/"+acct+"/balance", nil, withToken(conn.AccessToken)); r.Status != 404 && r.Status != 409 {
			t.Errorf("partner balance after the purge: %d", r.Status)
		}
		checkSettled(t, s.app)
	})

	s.run("leave keeps the money", func(s *Stack) {
		id := "purge-leave"
		s.pay("bank-a", s.persona("tunde").Accounts[0], id, s.openAccount(s.join("tunde", id), "current"), "5000")
		if _, err := s.applyWithout(id, map[string]any{"prune": true, "money": map[string]any{"money": "leave"}}); err != nil {
			t.Fatal(err)
		}
		s.ctl("POST", "/nip/settle-now", nil)
		s.waitGone("bank", id)
		lines := s.ctl("GET", "/closures/"+id, nil)["lines"].([]any)
		if len(lines) != 1 || lines[0].(map[string]any)["outcome"] != "left" || lines[0].(map[string]any)["amount_minor"] != float64(500_000) {
			t.Errorf("purge with --money leave: %v", lines)
		}
		checkSettled(t, s.app)
	})
}
