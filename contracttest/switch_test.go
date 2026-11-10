package contracttest

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/world"
)

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

func TestNIPSwitch(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newStack(t)
	from, to := s.persona("adaeze").Accounts[0], s.persona("ngozi").Accounts[0]

	enquiry := func(code, nuban string) map[string]any {
		return s.ctl("GET", "/nip/name-enquiry?bank_code="+code+"&account_number="+nuban, nil)
	}
	if ne := enquiry("999002", to); ne["response_code"] != "00" || ne["account_name"] != "Ngozi Fabrics Ventures" {
		t.Fatalf("name enquiry: %v", ne)
	}
	for name, c := range map[string]struct {
		ne   map[string]any
		code any
	}{
		"unknown account": {enquiry("999002", world.NUBAN("902", 99)), "25"},
		"bad check digit": {enquiry("999002", "0000000001"), "07"},
		"short number":    {enquiry("999002", "123"), "07"},
		"unknown bank":    {enquiry("000111", to), nil},
		"no such bank":    {enquiry("999003", world.NUBAN("903", 1)), nil},
	} {
		if c.ne["response_code"] != c.code {
			t.Fatalf("%s: %v", name, c.ne)
		}
	}
	if ne := enquiry("000111", to); ne["message"] != "Bank code 000111 is not in the NIBSS directory." {
		t.Fatalf("unknown bank: %v", ne)
	}
	s.ctl("POST", "/banks/bank-b/suspend", nil)
	if ne := enquiry("999002", to); ne["response_code"] != "91" {
		t.Fatalf("suspended bank: %v", ne)
	}
	if _, err := s.payTry("bank-a", from, "bank-b", to, "100", nil); err == nil || !strings.Contains(err.Error(), `"response_code":"91"`) {
		t.Fatalf("transfer to a suspended bank: %v", err)
	}
	s.ctl("POST", "/banks/bank-b/resume", nil)
	if ne := enquiry("999002", to); ne["response_code"] != "00" {
		t.Fatalf("resumed bank: %v", ne)
	}

	key := map[string]string{"Idempotency-Key": "k-1"}
	first, err := s.payTry("bank-a", from, "bank-b", to, "1500", key)
	if err != nil {
		t.Fatal(err)
	}
	sid := fmt.Sprint(first["session_id"])
	if len(sid) != 30 || first["response_code"] != "00" || !strings.HasPrefix(sid, "999001") {
		t.Fatalf("first send: %v", first)
	}
	again, resp, err := s.ctlWith("POST", "/banks/bank-a/transfers", map[string]string{"from": from, "to_bank": "bank-b", "to": to, "amount": "1500"}, key)
	if err != nil || resp.Header.Get("Idempotent-Replayed") != "true" || fmt.Sprint(again) != fmt.Sprint(first) {
		t.Fatalf("repeat by key: %v %v", again, err)
	}
	if _, err := s.payTry("bank-a", from, "bank-b", to, "1600", key); err == nil || !strings.Contains(err.Error(), "422") {
		t.Fatalf("key reused for another transfer: %v", err)
	}
	s.advance("48h")
	if n := s.sessionCredits("bank-b", to, sid); n != 1 {
		t.Fatalf("the beneficiary was credited %d times", n)
	}

	tsq := s.ctl("GET", "/nip/sessions/"+sid, nil)
	if tsq["status"] != "00" || tsq["outcome"] != "credited" || tsq["amount_minor"] != float64(150000) {
		t.Fatalf("status query: %v", tsq)
	}
	if _, err := s.ctlTry("GET", "/nip/sessions/"+strings.Repeat("1", 30), nil); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("unknown session: %v", err)
	}

	pos := s.position("bank-a")["position_minor"].(float64)
	s.setCap("bank-a", int64(pos)+100000)
	over, err := s.payTry("bank-a", from, "bank-b", to, "1500", nil)
	if err == nil || over["response_code"] != "61" {
		t.Fatalf("over the cap: %v %v", over, err)
	}
	s.advance("72h")
	s.setCap("bank-a", int64(s.position("bank-a")["position_minor"].(float64))+150000)
	if _, err := s.payTry("bank-a", from, "bank-b", to, "1500", nil); err != nil {
		t.Fatalf("after the session settled: %v", err)
	}

	seen := map[string]bool{}
	for _, id := range []string{"adaeze", "tunde", "ngozi", "chidi"} {
		bvn := s.persona(id).BVN
		if len(bvn) != 11 || !strings.HasPrefix(bvn, "229") || seen[bvn] {
			t.Fatalf("%s has BVN %q", id, bvn)
		}
		seen[bvn] = true
	}
}

func TestNetDebitPositionCountsAllTransfers(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newStack(t)
	from, to := s.persona("adaeze").Accounts[0], s.persona("ngozi").Accounts[0]
	before := s.position("bank-a")["open_outward_minor"].(float64)
	s.pay("bank-a", from, "bank-b", to, "7000")
	if got := s.position("bank-a")["open_outward_minor"].(float64); got != before+700000 {
		t.Fatalf("a transfer moved the open outward flow from %v to %v", before, got)
	}
	s.setCap("bank-a", 1)
	if _, err := s.payTry("bank-a", from, "bank-b", to, "1", nil); err == nil || !strings.Contains(err.Error(), "net_debit_cap_exceeded") {
		t.Fatalf("the cap let a transfer through: %v", err)
	}
	row := s.position("bank-a")
	if row["cap_source"] != "configured" || row["collateral_minor"] != nil {
		t.Fatalf("a cap that was set and collateral that was not: %v", row)
	}
	if def := s.position("bank-b"); def["cap_source"] != "simulator_default" || def["collateral_minor"] != nil {
		t.Fatalf("the default cap: %v", def)
	}
	s.ctl("POST", "/banks/bank-a/net-debit-cap", map[string]string{"cap": "9000000", "collateral": "9500000"})
	if row := s.position("bank-a"); row["collateral_minor"].(float64) != 950000000 {
		t.Fatalf("collateral: %v", row)
	}
	for _, bad := range []map[string]string{{}, {"cap": "abc"}, {"cap": "10", "collateral": "-1"}} {
		if _, err := s.ctlTry("POST", "/banks/bank-a/net-debit-cap", bad); err == nil || !strings.Contains(err.Error(), "HTTP 400") {
			t.Fatalf("limits %v: %v", bad, err)
		}
	}
	if _, err := s.ctlTry("POST", "/banks/nowhere/net-debit-cap", map[string]string{"cap": "10"}); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("unknown bank: %v", err)
	}
}

func TestEveryTransferPathIsIdempotent(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newStack(t)
	adaeze := s.persona("adaeze")
	from, own, other := adaeze.Accounts[0], adaeze.Accounts[1], s.persona("tunde").Accounts[0]
	debits := func() int {
		n := 0
		for _, l := range s.ctl("GET", "/banks/bank-a/accounts/"+from+"/statement?limit=1000", nil)["data"].([]any) {
			if l.(map[string]any)["amount_minor"] == float64(-123400) {
				n++
			}
		}
		return n
	}
	send := func(key string, body map[string]string) (map[string]any, *http.Response, error) {
		return s.ctlWith("POST", "/banks/bank-a/transfers", body, map[string]string{"Idempotency-Key": key})
	}
	body := map[string]string{"from": from, "to": other, "amount": "1234"}
	first, _, err := send("intra-1", body)
	if err != nil {
		t.Fatal(err)
	}
	again, resp, err := send("intra-1", body)
	if err != nil || resp.Header.Get("Idempotent-Replayed") != "true" || fmt.Sprint(again) != fmt.Sprint(first) {
		t.Fatalf("intra-bank retry: %v %v", again, err)
	}
	if n := debits(); n != 1 {
		t.Fatalf("the sender was debited %d times", n)
	}
	if _, _, err := send("intra-1", map[string]string{"from": from, "to": other, "amount": "1235"}); err == nil || !strings.Contains(err.Error(), "idempotency_key_reused") {
		t.Fatalf("key reused for another transfer: %v", err)
	}
	if _, _, err := send(strings.Repeat("k", 256), body); err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("a key of 256 characters: %v", err)
	}
	broke := map[string]string{"from": from, "to": own, "amount": "999999999"}
	_, _, err1 := send("broke-1", broke)
	_, resp, err2 := send("broke-1", broke)
	if err1 == nil || err2 == nil || resp.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("a failed transfer retried with its key: %v %v", err1, err2)
	}
}

func TestStatusQueryReportsReversal(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newStack(t)
	from, to := s.persona("adaeze").Accounts[0], s.persona("ngozi").Accounts[0]
	s.ctl("POST", "/faults", map[string]any{"kind": "timeout", "bank": "bank-b", "outcome": "failed", "count": 1})
	sent, _ := s.payTry("bank-a", from, "bank-b", to, "25000", nil)
	sid := fmt.Sprint(sent["session_id"])
	if tsq := s.ctl("GET", "/nip/sessions/"+sid, nil); tsq["status"] == "00" {
		t.Fatalf("a timed-out transfer: %v then %v", sent, tsq)
	}
	s.advance("3h")
	tsq := s.ctl("GET", "/nip/sessions/"+sid, nil)
	if tsq["outcome"] != "reversed" || tsq["resolved_at"] == nil {
		t.Fatalf("after the reversal: %v", tsq)
	}
	if n := s.sessionCredits("bank-b", to, sid); n != 0 {
		t.Fatalf("the beneficiary was credited %d times", n)
	}
}

func TestBankCodesAreComparedAsSixDigits(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newStack(t)
	for _, code := range []string{"901", "000901", "902", "123456", "1234", "12", "90a"} {
		if _, err := s.ctlTry("POST", "/banks", map[string]any{"name": "Newcomer " + code, "nuban_code": code}); err == nil || !strings.Contains(err.Error(), "HTTP 422") {
			t.Fatalf("nuban_code %q: %v", code, err)
		}
	}
}
