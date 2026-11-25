package contracttest

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/core"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

const roomyCap = 1_000_000_000_000_00

func minorToNaira(m int64) string {
	return fmt.Sprintf("%d.%02d", m/100, m%100)
}

func (s *Stack) position(bank string) map[string]any {
	s.t.Helper()
	for _, d := range s.ctl("GET", "/nip/positions", nil)["data"].([]any) {
		if row := d.(map[string]any); row["bank_id"] == bank {
			return row
		}
	}
	s.t.Fatalf("no position for %s", bank)
	return nil
}

func (s *Stack) setCap(bank string, minor int64) {
	s.t.Helper()
	s.ctl("POST", "/banks/"+bank+"/net-debit-cap", map[string]string{"cap": minorToNaira(minor)})
	waitFor(s.t, waitLimit, "the limits of "+bank+" to reach NIBSS", func() bool {
		r := s.ctl("GET", "/resources/bank/"+bank, nil)
		return r["observed_generation"] == r["generation"]
	})
}

func (s *Stack) sessionCredits(bank, nuban, sid string) int {
	s.t.Helper()
	n := 0
	for _, l := range s.ctl("GET", "/banks/"+bank+"/accounts/"+nuban+"/statement?limit=1000", nil)["data"].([]any) {
		if m := l.(map[string]any); m["session_id"] == sid && m["amount_minor"].(float64) > 0 {
			n++
		}
	}
	return n
}

func checkSettled(t *testing.T, app *core.App) core.SettlementCheck {
	t.Helper()
	c, err := app.CheckSettlement(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Problems) > 0 {
		t.Fatalf("settlement: %v", c.Problems)
	}
	return c
}

func (s *Stack) suspended(bank string) {
	s.t.Helper()
	s.ctl("POST", "/banks/"+bank+"/suspend", nil)
	s.t.Cleanup(func() {
		s.ctl("POST", "/banks/"+bank+"/resume", nil)
		s.waitPhase(bank, core.PhaseActive)
	})
}

func TestNIPSwitch(t *testing.T) {
	s := sharedStack(t, 1)
	s.needsNodes()
	t.Cleanup(func() { s.setCap("bank-a", roomyCap) })
	adaeze, ngozi := s.persona("adaeze"), s.persona("ngozi")
	from, to := adaeze.Accounts[0], ngozi.Accounts[0]
	enquiry := func(code, nuban string) map[string]any {
		return s.ctl("GET", "/nip/name-enquiry?bank_code="+code+"&account_number="+nuban, nil)
	}

	s.run("name enquiry and refusals", func(s *Stack) {
		if ne := enquiry("000002", to); ne["response_code"] != "00" || ne["account_name"] != "Ngozi Fabrics Ventures" {
			t.Fatalf("name enquiry: %v", ne)
		}
		for name, c := range map[string]struct {
			ne   map[string]any
			code any
		}{
			"unknown account": {enquiry("000002", world.NUBAN("002", 99999)), "25"},
			"bad check digit": {enquiry("000002", "0000000001"), "07"},
			"unknown bank":    {enquiry("000111", to), "96"},
		} {
			if c.ne["response_code"] != c.code {
				t.Errorf("%s: %v", name, c.ne)
			}
		}
		if ne := enquiry("000111", to); ne["message"] != "Bank code 000111 is not in the NIBSS directory." {
			t.Errorf("unknown bank: %v", ne)
		}
		if _, err := s.payTry("bank-a", from, "bank-b", "123", "100", nil); err == nil || !strings.Contains(err.Error(), `"response_code":"07"`) || !strings.Contains(err.Error(), "That is not a valid account number, so nothing was debited.") {
			t.Errorf("transfer to a short account number: %v", err)
		}
		s.suspended("bank-b")
		if ne := enquiry("000002", to); ne["response_code"] != "91" {
			t.Errorf("suspended bank: %v", ne)
		}
		if _, err := s.payTry("bank-a", from, "bank-b", to, "100", nil); err == nil || !strings.Contains(err.Error(), `"response_code":"91"`) {
			t.Errorf("transfer to a suspended bank: %v", err)
		}
	})

	s.run("send, replay and status query", func(s *Stack) {
		key := map[string]string{"Idempotency-Key": "k-" + s.flow}
		first, err := s.payTry("bank-a", from, "bank-b", to, "1500", key)
		if err != nil {
			t.Fatal(err)
		}
		sid := fmt.Sprint(first["session_id"])
		if len(sid) != 30 || first["response_code"] != "00" || !strings.HasPrefix(sid, "000001") {
			t.Fatalf("first send: %v", first)
		}
		again, _, err := s.ctlWith("POST", "/banks/bank-a/transfers", map[string]string{"from": from, "to_bank": "bank-b", "to": to, "amount": "1500"}, key)
		if err != nil || fmt.Sprint(again) != fmt.Sprint(first) {
			t.Fatalf("repeat by key: %v %v", again, err)
		}
		if _, err := s.payTry("bank-a", from, "bank-b", to, "1600", key); err == nil || !strings.Contains(err.Error(), "422") {
			t.Errorf("key reused for another transfer: %v", err)
		}
		s.ctl("POST", "/nip/sessions/"+sid+"/status", nil)
		if n := s.sessionCredits("bank-b", to, sid); n != 1 {
			t.Errorf("the beneficiary was credited %d times", n)
		}
		if tsq := s.ctl("GET", "/nip/sessions/"+sid, nil); tsq["status"] != "00" || tsq["outcome"] != "credited" || tsq["amount_minor"] != float64(150000) {
			t.Errorf("status query: %v", tsq)
		}
		if _, err := s.ctlTry("GET", "/nip/sessions/"+strings.Repeat("1", 30), nil); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
			t.Errorf("unknown session: %v", err)
		}
		debits := func() int {
			n := 0
			for _, l := range s.ctl("GET", "/banks/bank-a/accounts/"+from+"/statement?limit=1000", nil)["data"].([]any) {
				if l.(map[string]any)["amount_minor"] == float64(-123400) {
					n++
				}
			}
			return n
		}
		intra := map[string]string{"from": from, "to": s.persona("tunde").Accounts[0], "amount": "1234"}
		send := func(key string, body map[string]string) error {
			_, _, err := s.ctlWith("POST", "/banks/bank-a/transfers", body, map[string]string{"Idempotency-Key": key})
			return err
		}
		if err := send("intra-"+s.flow, intra); err != nil {
			t.Fatal(err)
		}
		if err := send("intra-"+s.flow, intra); err != nil || debits() != 1 {
			t.Errorf("intra-bank retry: %v, debited %d times", err, debits())
		}
		other := map[string]string{"from": from, "to": intra["to"], "amount": "1235"}
		if err := send("intra-"+s.flow, other); err == nil || !strings.Contains(err.Error(), "idempotency_key_reused") {
			t.Errorf("key reused for another transfer: %v", err)
		}
		if err := send(strings.Repeat("k", 256), intra); err == nil || !strings.Contains(err.Error(), "HTTP 400") {
			t.Errorf("a key of 256 characters: %v", err)
		}
	})

	s.run("a timeout holds a transfer pending", func(s *Stack) {
		before := s.position("bank-a")["open_outward_minor"].(float64)
		s.ctl("POST", "/faults", map[string]any{"kind": "timeout", "bank": "bank-b", "outcome": "credited", "count": 1})
		sent, _ := s.payTry("bank-a", from, "bank-b", to, "2000", nil)
		sid := fmt.Sprint(sent["session_id"])
		if sent["status"] == "success" || s.ctl("GET", "/nip/sessions/"+sid, nil)["status"] == "00" {
			t.Fatalf("a timed-out transfer finished: %v", sent)
		}
		if got := s.position("bank-a")["open_outward_minor"].(float64); got != before+200000 {
			t.Errorf("a pending session moved the open outward flow from %v to %v", before, got)
		}
		s.setCap("bank-a", int64(s.position("bank-a")["position_minor"].(float64))+100000)
		over, err := s.payTry("bank-a", from, "bank-b", to, "2000", nil)
		if err != nil || over["status"] != "reversed" || over["response_code"] != "61" {
			t.Errorf("a pending session did not count against the cap: %v %v", over, err)
		}
		s.ctl("POST", "/nip/sessions/"+sid+"/status", nil)
		if tsq := s.ctl("GET", "/nip/sessions/"+sid, nil); tsq["status"] != "00" || tsq["outcome"] != "credited" {
			t.Errorf("status query of the held transfer: %v", tsq)
		}
		if n := s.sessionCredits("bank-b", to, sid); n != 1 {
			t.Errorf("the beneficiary was credited %d times", n)
		}
	})

	s.run("a failed credit is reversed", func(s *Stack) {
		s.setCap("bank-a", roomyCap)
		before := s.balances(adaeze)[from]
		s.ctl("POST", "/faults", map[string]any{"kind": "timeout", "bank": "bank-b", "outcome": "failed", "count": 1})
		sent, _ := s.payTry("bank-a", from, "bank-b", to, "25000", nil)
		sid := fmt.Sprint(sent["session_id"])
		if tsq := s.ctl("GET", "/nip/sessions/"+sid, nil); tsq["status"] == "00" {
			t.Fatalf("a timed-out transfer: %v then %v", sent, tsq)
		}
		s.ctl("POST", "/nip/sessions/"+sid+"/status", nil)
		waitFor(t, waitLimit, "the status query to fail the session", func() bool {
			return s.ctl("GET", "/nip/sessions/"+sid, nil)["status"] == "25"
		})
		if tsq := s.ctl("GET", "/nip/sessions/"+sid, nil); tsq["outcome"] != "failed" || tsq["resolved_at"] == nil {
			t.Errorf("after the failure: %v", tsq)
		}
		waitFor(t, waitLimit, "the sender to be reversed", func() bool { return s.balances(adaeze)[from] == before })
		if n := s.sessionCredits("bank-b", to, sid); n != 0 {
			t.Errorf("the beneficiary was credited %d times", n)
		}
	})

	s.run("net debit cap", func(s *Stack) {
		pos := s.position("bank-a")["position_minor"].(float64)
		s.setCap("bank-a", int64(pos)+100000)
		if over, err := s.payTry("bank-a", from, "bank-b", to, "1500", nil); err != nil || over["status"] != "reversed" || over["response_code"] != "61" {
			t.Errorf("over the cap: %v %v", over, err)
		}
		s.ctl("POST", "/nip/settle-now", nil)
		s.setCap("bank-a", int64(s.position("bank-a")["position_minor"].(float64))+150000)
		if _, err := s.payTry("bank-a", from, "bank-b", to, "1500", nil); err != nil {
			t.Errorf("after the session settled: %v", err)
		}
		s.setCap("bank-a", 1)
		row := s.position("bank-a")
		if row["cap_source"] != "configured" || row["collateral_minor"] != nil {
			t.Errorf("a cap that was set and collateral that was not: %v", row)
		}
		if def := s.position("bank-b"); def["cap_source"] != "default" || def["collateral_minor"] != nil {
			t.Errorf("the default cap: %v", def)
		}
		s.ctl("POST", "/banks/bank-a/net-debit-cap", map[string]string{"cap": "9000000", "collateral": "9500000"})
		if row := s.position("bank-a"); row["collateral_minor"].(float64) != 950000000 {
			t.Errorf("collateral: %v", row)
		}
		for _, bad := range []map[string]string{{}, {"cap": "abc"}, {"cap": "10", "collateral": "-1"}} {
			if _, err := s.ctlTry("POST", "/banks/bank-a/net-debit-cap", bad); err == nil || !strings.Contains(err.Error(), "HTTP 400") {
				t.Errorf("limits %v: %v", bad, err)
			}
		}
		if _, err := s.ctlTry("POST", "/banks/nowhere/net-debit-cap", map[string]string{"cap": "10"}); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
			t.Errorf("unknown bank: %v", err)
		}
	})
}

func TestSettlement(t *testing.T) {
	s := sharedStack(t, 1)
	s.needsNodes()
	from, to := s.persona("adaeze").Accounts[0], s.persona("ngozi").Accounts[0]

	s.run("a cutoff posts once and a retry replays", func(s *Stack) {
		first := checkSettled(t, s.app)
		cfg, err := pgxpool.ParseConfig(s.cfg.DatabaseURL)
		if err != nil {
			t.Fatal(err)
		}
		cfg.ConnConfig.Database = config.DatabaseName(s.cfg.Dir, "nibss")
		db, err := pgxpool.NewWithConfig(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		tag, err := db.Exec(t.Context(), `UPDATE settlement_sessions SET posted_at = NULL`)
		if err != nil || int(tag.RowsAffected()) != first.Sessions {
			t.Fatalf("made %d of %d cutoffs unposted: %v", tag.RowsAffected(), first.Sessions, err)
		}
		if again := checkSettled(t, s.app); again.Sessions != first.Sessions {
			t.Errorf("a retry posted %d cutoffs, want the same %d", again.Sessions, first.Sessions)
		}
	})

	s.run("settlement runs alongside the app", func(s *Stack) {
		s.pay("bank-a", from, "bank-b", to, "1500")
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(3)
			go func() {
				defer wg.Done()
				if _, err := s.ctlTry("POST", "/nip/settle-now", nil); err != nil {
					t.Errorf("settle now: %v", err)
				}
			}()
			go func() {
				defer wg.Done()
				s.app.Step(context.Background())
			}()
			go func() {
				defer wg.Done()
				if c, err := s.app.CheckSettlement(context.Background()); err != nil || len(c.Problems) > 0 {
					t.Errorf("settlement: %v %v", err, c.Problems)
				}
			}()
		}
		wg.Wait()
		checkSettled(t, s.app)
	})
}
