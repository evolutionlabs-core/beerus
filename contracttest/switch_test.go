package contracttest

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
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

func TestNIPSwitch(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newStack(t)
	w := s.app.World()
	from, to := w.AccountsAt("adaeze", "bank-a")[0], w.AccountsAt("ngozi", "bank-b")[0]

	enquiry := func(code, nuban string) map[string]any {
		return s.ctl("GET", "/nip/name-enquiry?bank_code="+code+"&account_number="+nuban, nil)
	}
	if ne := enquiry("999002", to.NUBAN); ne["response_code"] != "00" || !strings.Contains(fmt.Sprint(ne["account_name"]), "NGOZI") {
		t.Fatalf("name enquiry: %v", ne)
	}
	for name, ne := range map[string]map[string]any{
		"unknown account": enquiry("999002", "0000000001"),
		"short number":    enquiry("999002", "123"),
		"unknown bank":    enquiry("000111", to.NUBAN),
	} {
		if ne["response_code"] != "07" {
			t.Fatalf("%s: %v", name, ne)
		}
	}
	if ne := enquiry("999003", world.NUBAN("999003", 123456)); ne["response_code"] != "00" {
		t.Fatalf("outside bank: %v", ne)
	}
	checked := 0
	for _, l := range w.LinesFor(from.Key, s.app.Clock().Now()) {
		if l.Counterparty == nil || slices.ContainsFunc(s.cfg.Banks, func(b *config.Bank) bool { return b.NIPCode == l.Counterparty.BankCode }) {
			continue
		}
		if ne := enquiry(l.Counterparty.BankCode, l.Counterparty.AccountNumber); ne["response_code"] != "00" {
			t.Fatalf("name enquiry on a generated counterparty: %v", ne)
		}
		if checked++; checked == 20 {
			break
		}
	}
	if checked == 0 {
		t.Fatal("no generated counterparties")
	}
	if ne := enquiry("999003", "0123456789"); ne["response_code"] != "07" {
		t.Fatalf("outside bank, bad check digit: %v", ne)
	}
	s.ctl("POST", "/banks/bank-b/suspend", nil)
	if ne := enquiry("999002", to.NUBAN); ne["response_code"] != "91" {
		t.Fatalf("suspended bank: %v", ne)
	}
	if _, err := s.ctlTry("POST", "/transfers", map[string]any{"from": from.Key, "to": to.Key, "amount": "100"}); err == nil || !strings.Contains(err.Error(), `"response_code":"91"`) {
		t.Fatalf("transfer to a suspended bank: %v", err)
	}
	s.ctl("POST", "/banks/bank-b/resume", nil)
	if ne := enquiry("999002", to.NUBAN); ne["response_code"] != "00" {
		t.Fatalf("resumed bank: %v", ne)
	}

	body := map[string]any{"from": from.Key, "to": to.Key, "amount": "1500"}
	first, _, err := s.ctlWith("POST", "/transfers", body, map[string]string{"Idempotency-Key": "k-1"})
	if err != nil {
		t.Fatal(err)
	}
	sid := fmt.Sprint(first["session_id"])
	if len(sid) != 30 || first["response_code"] != "00" || !strings.HasPrefix(sid, "999001") {
		t.Fatalf("first send: %v", first)
	}
	again, resp, err := s.ctlWith("POST", "/transfers", body, map[string]string{"Idempotency-Key": "k-1"})
	if err != nil || resp.Header.Get("Idempotent-Replayed") != "true" || fmt.Sprint(again) != fmt.Sprint(first) {
		t.Fatalf("repeat by key: %v %v", again, err)
	}
	body["session_id"] = sid
	again, _, err = s.ctlWith("POST", "/transfers", body, nil)
	if err != nil || fmt.Sprint(again["session_id"]) != sid {
		t.Fatalf("repeat by session: %v %v", again, err)
	}
	delete(body, "session_id")
	if _, _, err := s.ctlWith("POST", "/transfers", map[string]any{"from": from.Key, "to": to.Key, "amount": "1600"}, map[string]string{"Idempotency-Key": "k-1"}); err == nil || !strings.Contains(err.Error(), "422") {
		t.Fatalf("key reused for another transfer: %v", err)
	}
	now, _ := time.Parse(time.RFC3339, fmt.Sprint(s.ctl("GET", "/clock", nil)["business_time"]))
	credits := 0
	for _, tr := range w.Trace(sid, now.Add(48*time.Hour)) {
		if tr.Account.Key == to.Key && tr.Line.Amount > 0 {
			credits++
		}
	}
	if credits != 1 {
		t.Fatalf("the beneficiary was credited %d times", credits)
	}

	tsq := s.ctl("GET", "/nip/transfers/"+sid, nil)
	if tsq["status"] != "success" || tsq["response_code"] != "00" {
		t.Fatalf("status query: %v", tsq)
	}
	missing, err := s.ctlTry("GET", "/nip/transfers/"+strings.Repeat("1", 30), nil)
	if err == nil || missing["response_code"] != "25" {
		t.Fatalf("unknown session: %v %v", missing, err)
	}

	pos := s.position("bank-a")["position_minor"].(float64)
	s.setCap("bank-a", int64(pos)+100000)
	over, err := s.ctlTry("POST", "/transfers", map[string]any{"from": from.Key, "to": to.Key, "amount": "1500"})
	if err == nil || over["response_code"] != "61" {
		t.Fatalf("over the cap: %v %v", over, err)
	}
	if tsq := s.ctl("GET", "/nip/transfers/"+fmt.Sprint(over["session_id"]), nil); tsq["status"] != "failed" || tsq["response_code"] != "61" {
		t.Fatalf("status of the rejected transfer: %v", tsq)
	}
	s.advance("72h")
	s.setCap("bank-a", int64(s.position("bank-a")["position_minor"].(float64))+150000)
	if _, err := s.ctlTry("POST", "/transfers", map[string]any{"from": from.Key, "to": to.Key, "amount": "1500"}); err != nil {
		t.Fatalf("after the session settled: %v", err)
	}

	seen := map[string]bool{}
	ps := s.ctl("GET", "/personas", nil)["data"].([]any)
	for _, p := range ps {
		bvn := fmt.Sprint(p.(map[string]any)["bvn"])
		if len(bvn) != 11 || !strings.HasPrefix(bvn, "229") || seen[bvn] {
			t.Fatalf("bvn %q", bvn)
		}
		seen[bvn] = true
	}
}

func TestNetDebitPositionCountsAllTransfers(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newStack(t)
	w := s.app.World()
	from, to := w.AccountsAt("adaeze", "bank-a")[0], w.AccountsAt("ngozi", "bank-b")[0]
	before := s.position("bank-a")["open_outward_minor"].(float64)
	now, _ := time.Parse(time.RFC3339, fmt.Sprint(s.ctl("GET", "/clock", nil)["business_time"]))
	if _, err := w.Transfer(now, world.TransferRequest{From: from.Key, To: to.Key, Amount: 700000}); err != nil {
		t.Fatal(err)
	}
	if got := s.position("bank-a")["open_outward_minor"].(float64); got != before+700000 {
		t.Fatalf("a transfer that did not come through the control API moved the open outward flow from %v to %v", before, got)
	}
	s.setCap("bank-a", 1)
	if _, err := s.ctlTry("POST", "/transfers", map[string]any{"from": from.Key, "to": to.Key, "amount": "1"}); err == nil || !strings.Contains(err.Error(), "net_debit_cap_exceeded") {
		t.Fatalf("the cap ignored a transfer made outside the control API: %v", err)
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
	w := s.app.World()
	from, own, other := w.AccountsAt("adaeze", "bank-a")[0], w.AccountsAt("adaeze", "bank-a")[1], w.AccountsAt("tunde", "bank-a")[0]
	debits := func() int {
		n := 0
		ls, err := w.Statement(from.Key, s.app.Clock().Now().Add(24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range ls {
			if l.Amount == -123400 {
				n++
			}
		}
		return n
	}
	send := func(key string, body map[string]any) (map[string]any, *http.Response, error) {
		return s.ctlWith("POST", "/transfers", body, map[string]string{"Idempotency-Key": key})
	}
	body := map[string]any{"from": from.Key, "to": other.Key, "amount": "1234"}
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
	if _, _, err := send("intra-1", map[string]any{"from": from.Key, "to": other.Key, "amount": "1235"}); err == nil || !strings.Contains(err.Error(), "idempotency_key_reused") {
		t.Fatalf("key reused for another transfer: %v", err)
	}
	if _, _, err := send(strings.Repeat("k", 256), body); err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("a key of 256 characters: %v", err)
	}
	broke := map[string]any{"from": from.Key, "to": own.Key, "amount": "999999999"}
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
	acct := s.app.World().AccountsAt("adaeze", "bank-a")[0]
	s.ctl("POST", "/scenarios/debit_then_reversal", map[string]any{"account_id": acct.Key})
	now, _ := time.Parse(time.RFC3339, fmt.Sprint(s.ctl("GET", "/clock", nil)["business_time"]))
	sid := ""
	ls, err := s.app.World().Statement(acct.Key, now.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range ls {
		if l.Channel == "nip" && l.SessionID != "" && l.Amount == -2500000 && !l.At.Before(now) {
			sid = l.SessionID
		}
	}
	if sid == "" {
		t.Fatal("the scenario made no transfer")
	}
	if tsq := s.ctl("GET", "/nip/transfers/"+sid, nil); tsq["status"] != "success" {
		t.Fatalf("before the reversal: %v", tsq)
	}
	s.advance("3h")
	if tsq := s.ctl("GET", "/nip/transfers/"+sid, nil); tsq["status"] != "reversed" || tsq["reversed_at"] == nil {
		t.Fatalf("after the reversal: %v", tsq)
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
