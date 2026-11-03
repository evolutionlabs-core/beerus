package contracttest

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

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
	if ne := enquiry("999003", "0123456789"); ne["response_code"] != "00" {
		t.Fatalf("outside bank: %v", ne)
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

	s.cfg.Bank("bank-a").NetDebitCap = 200000
	over, err := s.ctlTry("POST", "/transfers", map[string]any{"from": from.Key, "to": to.Key, "amount": "1500"})
	if err == nil || over["response_code"] != "51" {
		t.Fatalf("over the cap: %v %v", over, err)
	}
	if tsq := s.ctl("GET", "/nip/transfers/"+fmt.Sprint(over["session_id"]), nil); tsq["status"] != "failed" || tsq["response_code"] != "51" {
		t.Fatalf("status of the rejected transfer: %v", tsq)
	}
	s.advance("72h")
	if _, err := s.ctlTry("POST", "/transfers", map[string]any{"from": from.Key, "to": to.Key, "amount": "1500"}); err != nil {
		t.Fatalf("after the session settled: %v", err)
	}

	ps := s.ctl("GET", "/personas", nil)["data"].([]any)
	for _, p := range ps {
		if bvn := fmt.Sprint(p.(map[string]any)["bvn"]); len(bvn) != 11 || !strings.HasPrefix(bvn, "229") {
			t.Fatalf("bvn %q", bvn)
		}
	}
}
