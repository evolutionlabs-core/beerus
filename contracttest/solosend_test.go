package contracttest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/banktest"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

const (
	soloSendClient = 60 * time.Second
	soloSendAmount = 2_500_000
	soloBigCap     = 1_000_000_000_000
	soloBelowCap   = 3_000_000
	soloRestartFor = 30 * time.Second
)

var sessionText = regexp.MustCompile(`^[0-9]{30}$`)

type soloSend struct {
	*soloNIBSS
	clients map[string]*http.Client
	banks   *http.Client
	fees    int64
}

func startSoloSend(t *testing.T, tsqAfter time.Duration) *soloSend {
	t.Helper()
	s, clients := startSoloNIBSS(t, tsqAfter, "bank-a", "bank-b")
	out := &soloSend{soloNIBSS: s, clients: clients, banks: &http.Client{Timeout: soloSendClient}}
	charges := banktest.Fees(t).ForTransfer(soloSendAmount)
	out.fees = charges.Fee + charges.VAT + charges.StampDuty
	for _, id := range []string{"bank-a", "bank-b"} {
		m := s.members[id]
		register := fmt.Sprintf(`{"nip_code":%q,"nuban_code":%q,"name":%q,"type":"commercial","credit_url":"https://%s.localhost:%d","certificate_identity":%q}`, m.bank.NIPCode, m.bank.NUBANCode, m.bank.Name, id, m.bank.APIPort, id)
		if status, got := s.call("nip", s.plain, http.MethodPut, s.control+"/v1/control/banks/"+id, s.token, register); status != http.StatusOK {
			t.Fatalf("register %s: %d %s", id, status, got)
		}
		out.limit(id, soloBigCap)
	}
	return out
}

func (s *soloSend) limit(id string, cap int64) {
	s.t.Helper()
	body := fmt.Sprintf(`{"net_debit_cap_minor":%d,"collateral_minor":0}`, cap)
	want := body
	if status, got := s.call("nip", s.plain, http.MethodPut, s.control+"/v1/control/banks/"+id+"/limits", s.token, body); status != http.StatusOK || got != want {
		s.t.Fatalf("limits of %s: %d %s", id, status, got)
	}
}

func (s *soloSend) bankToken(id string) string {
	s.t.Helper()
	token, err := config.ReadSecret(filepath.Join(config.BankDir(s.dir, id), config.BankTokenFile))
	if err != nil {
		s.t.Fatal(err)
	}
	return token
}

func (s *soloSend) bankCall(id, method, path, key, body string) (int, http.Header, map[string]any) {
	s.t.Helper()
	header := http.Header{}
	if key != "" {
		header.Set("Idempotency-Key", key)
	}
	url := fmt.Sprintf("http://127.0.0.1:%d%s", s.members[id].bank.Port, path)
	status, h, raw := s.callWith("participant", s.banks, method, url, s.bankToken(id), body, header)
	out := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		s.t.Fatalf("%s %s answered %d %s", method, path, status, raw)
	}
	return status, h, out
}

func (s *soloSend) fault(id, body string) {
	s.t.Helper()
	if status, _, got := s.bankCall(id, http.MethodPost, "/v1/faults", "fault-"+body, body); status != http.StatusCreated {
		s.t.Fatalf("fault at %s: %d %v", id, status, got)
	}
}

func (s *soloSend) clear(id string) {
	s.t.Helper()
	if status, _, got := s.bankCall(id, http.MethodDelete, "/v1/faults", "", ""); status != http.StatusOK {
		s.t.Fatalf("clear faults at %s: %d %v", id, status, got)
	}
}

func (s *soloSend) balance(id string) int64 {
	s.t.Helper()
	m := s.members[id]
	bal, err := m.books.L.Balance(context.Background(), bank.CustomerAccount(m.nuban))
	if err != nil {
		s.t.Fatal(err)
	}
	return bal
}

func (s *soloSend) open(id string) int {
	s.t.Helper()
	var n int
	if err := s.members[id].books.L.Pool().QueryRow(context.Background(), `SELECT count(*) FROM nip_sends`).Scan(&n); err != nil {
		s.t.Fatal(err)
	}
	return n
}

func (s *soloSend) send(key string, amount int64) (int, http.Header, map[string]any) {
	s.t.Helper()
	body := fmt.Sprintf(`{"from":%q,"to_bank":"bank-b","to":%q,"amount":%q,"narration":"Rent"}`, s.members["bank-a"].nuban, s.members["bank-b"].nuban, fmt.Sprintf("%d.%02d", amount/100, amount%100))
	return s.bankCall("bank-a", http.MethodPost, "/v1/transfers", key, body)
}

func (s *soloSend) query(sid string) (int, map[string]any) {
	s.t.Helper()
	status, _, got := s.bankCall("bank-a", http.MethodPost, "/v1/nip/sends/"+sid+"/status-query", "", "")
	return status, got
}

func (s *soloSend) transferBody(sid string, amount int64, closure bool) string {
	return fmt.Sprintf(`{"session_id":%q,"from_code":"000014","to_code":"000015","from_nuban":%q,"to_nuban":%q,"from_name":"ADA OBI","amount_minor":%d,"narration":"Rent","closure":%v}`, sid, s.members["bank-a"].nuban, s.members["bank-b"].nuban, amount, closure)
}

func (s *soloSend) direct(client, path, body string) (int, http.Header, string) {
	s.t.Helper()
	return s.callWith("nip", s.clients[client], http.MethodPost, s.dirURL+path, "", body, nil)
}

func TestSoloSendThroughNIBSS(t *testing.T) {
	s := startSoloSend(t, soloTSQAfter)
	a0, b0 := s.balance("bank-a"), s.balance("bank-b")
	debit := int64(soloSendAmount) + s.fees
	errText := func(code, message string) string {
		return fmt.Sprintf(`{"error":{"code":%q,"message":%q,"request_id":"req_ID"}}`, code, message)
	}
	var sid, held, limited string
	unknown := "000014261115120000000000000099"
	rows := []struct {
		name   string
		run    func() (int, string)
		status int
		want   func() string
		a, b   func() int64
	}{
		{"success with charges on the sender", func() (int, string) {
			status, _, got := s.send("send-1", soloSendAmount)
			sid, _ = got["session_id"].(string)
			return status, fmt.Sprintf("%v %v %d %d %v", got["status"], got["response_code"], int64(got["sender_balance_after_minor"].(float64)), int64(got["total_debit_minor"].(float64)), got["to_name"])
		}, 200, func() string { return fmt.Sprintf("success 00 %d %d ADA OBI", a0-debit, debit) }, func() int64 { return a0 - debit }, func() int64 { return b0 + soloSendAmount }},
		{"a replay at the bank", func() (int, string) {
			status, h, got := s.send("send-1", soloSendAmount)
			return status, fmt.Sprintf("%v %v %v", h.Get("Idempotent-Replayed"), got["session_id"] == sid, got["status"])
		}, 200, func() string { return "true true success" }, func() int64 { return a0 - debit }, func() int64 { return b0 + soloSendAmount }},
		{"a replay at NIBSS", func() (int, string) {
			status, h, got := s.direct("bank-a", "/v1/transfers", s.transferBody(sid, soloSendAmount, false))
			var x map[string]any
			_ = json.Unmarshal([]byte(got), &x)
			return status, fmt.Sprintf("%v %v %v", h.Get("Idempotent-Replayed"), x["status"], x["outcome"])
		}, 200, func() string { return "true 00 credited" }, func() int64 { return a0 - debit }, func() int64 { return b0 + soloSendAmount }},
		{"the same session with another amount", func() (int, string) {
			status, _, got := s.direct("bank-a", "/v1/transfers", s.transferBody(sid, 1, false))
			return status, got
		}, 422, func() string {
			return errText("idempotency_key_reused", fmt.Sprintf("Session %s was first sent with amount_minor %d, and this request has 1. Send a new session_id, or repeat the first request.", sid, soloSendAmount))
		}, func() int64 { return a0 - debit }, func() int64 { return b0 + soloSendAmount }},
		{"a closure from an active bank", func() (int, string) {
			status, _, got := s.direct("bank-a", "/v1/transfers", s.transferBody("000014261115120000000000000001", soloSendAmount, true))
			return status, got
		}, 422, func() string {
			return errText("closure_not_allowed", "Bank 000014 is active, so it cannot send a closure transfer. Close the bank first, and send it again when the bank is terminating.")
		}, func() int64 { return a0 - debit }, func() int64 { return b0 + soloSendAmount }},
		{"a stranger certificate", func() (int, string) {
			status, _, got := s.direct("bank-z", "/v1/transfers", s.transferBody("000014261115120000000000000002", soloSendAmount, false))
			return status, got
		}, 403, func() string {
			return errText("forbidden", "This certificate may not send transfers. bank-z is not a bank NIBSS knows.")
		}, func() int64 { return a0 - debit }, func() int64 { return b0 + soloSendAmount }},
		{"another bank sending as bank A", func() (int, string) {
			status, _, got := s.direct("bank-b", "/v1/transfers", s.transferBody("000014261115120000000000000003", soloSendAmount, false))
			return status, got
		}, 403, func() string {
			return errText("forbidden", "This certificate may not send transfers for bank 000014.")
		}, func() int64 { return a0 - debit }, func() int64 { return b0 + soloSendAmount }},
		{"over the cap, reversed", func() (int, string) {
			s.limit("bank-a", soloBelowCap)
			status, _, got := s.send("send-over-cap", soloSendAmount)
			limited, _ = got["session_id"].(string)
			s.limit("bank-a", soloBigCap)
			return status, fmt.Sprintf("%v %v", got["status"], got["response_code"])
		}, 200, func() string { return "reversed 61" }, func() int64 { return a0 - debit }, func() int64 { return b0 + soloSendAmount }},
		{"the capped session is final at NIBSS", func() (int, string) {
			status, got := s.query(limited)
			return status, fmt.Sprintf("%v %v %v", got["status"], got["outcome"], got["tsq_attempts"])
		}, 200, func() string { return "61 failed 0" }, func() int64 { return a0 - debit }, func() int64 { return b0 + soloSendAmount }},
		{"bank B down, refused before the debit", func() (int, string) {
			s.fault("bank-b", `{"kind":"down"}`)
			status, _, got := s.send("send-down", soloSendAmount)
			return status, fmt.Sprintf("%v", got["error"].(map[string]any)["message"])
		}, 503, func() string { return "Bank B is not available right now, so nothing was debited." }, func() int64 { return a0 - debit }, func() int64 { return b0 + soloSendAmount }},
		{"bank B down at NIBSS, final 91", func() (int, string) {
			status, _, got := s.direct("bank-a", "/v1/transfers", s.transferBody("000014261115120000000000000004", soloSendAmount, false))
			var x map[string]any
			_ = json.Unmarshal([]byte(got), &x)
			s.clear("bank-b")
			return status, fmt.Sprintf("%v %v", x["status"], x["outcome"])
		}, 200, func() string { return "91 failed" }, func() int64 { return a0 - debit }, func() int64 { return b0 + soloSendAmount }},
		{"a timeout, then pending, then credited", func() (int, string) {
			s.fault("bank-b", `{"kind":"timeout","outcome":"credited","count":1}`)
			status, _, got := s.send("send-held", soloSendAmount)
			held, _ = got["session_id"].(string)
			return status, fmt.Sprintf("%v %v %v", got["status"], got["response_code"], got["next_check_at"] != nil)
		}, 200, func() string { return "pending pending true" }, func() int64 { return a0 - 2*debit }, func() int64 { return b0 + 2*soloSendAmount }},
		{"the status query credits it", func() (int, string) {
			status, got := s.query(held)
			return status, fmt.Sprintf("%v %v %v", got["status"], got["outcome"], got["tsq_attempts"])
		}, 200, func() string { return "00 credited 1" }, func() int64 { return a0 - 2*debit }, func() int64 { return b0 + 2*soloSendAmount }},
		{"a silent bank is never given up on, then answers 25 and is reversed once", func() (int, string) {
			s.fault("bank-b", `{"kind":"timeout","outcome":"unknown","count":3}`)
			_, _, sent := s.send("send-silent", soloSendAmount)
			silent := fmt.Sprint(sent["session_id"])
			_, first := s.query(silent)
			_, second := s.query(silent)
			_, third := s.query(silent)
			_, fourth := s.query(silent)
			return http.StatusOK, fmt.Sprintf("%v %v %v|%v %v|%v %v|%v %v", sent["status"], first["status"], first["tsq_attempts"], second["status"], second["tsq_attempts"], third["status"], third["tsq_attempts"], fourth["status"], fourth["tsq_attempts"])
		}, 200, func() string { return "pending pending 1|pending 2|25 3|25 3" }, func() int64 { return a0 - 2*debit }, func() int64 { return b0 + 2*soloSendAmount }},
		{"a status query for a session NIBSS never saw", func() (int, string) {
			status, _, got := s.direct("bank-a", "/v1/transfers/"+unknown+"/status-queries", "")
			return status, got
		}, 404, func() string {
			return errText("not_found", "NIBSS has no session "+unknown+". Send the transfer again with the same session_id.")
		}, func() int64 { return a0 - 2*debit }, func() int64 { return b0 + 2*soloSendAmount }},
		{"a status query for a send the bank never made", func() (int, string) {
			status, got := s.query(unknown)
			return status, fmt.Sprint(got["error"].(map[string]any)["message"])
		}, 404, func() string { return "This bank has no send with that session ID." }, func() int64 { return a0 - 2*debit }, func() int64 { return b0 + 2*soloSendAmount }},
		{"a short account number", func() (int, string) {
			body := fmt.Sprintf(`{"from":%q,"to_bank":"bank-b","to":"123456789","amount":"10"}`, s.members["bank-a"].nuban)
			status, _, got := s.bankCall("bank-a", http.MethodPost, "/v1/transfers", "send-short", body)
			return status, fmt.Sprint(got["error"].(map[string]any)["message"])
		}, 422, func() string { return "That is not a valid account number, so nothing was debited." }, func() int64 { return a0 - 2*debit }, func() int64 { return b0 + 2*soloSendAmount }},
	}
	for _, r := range rows {
		status, got := r.run()
		if status != r.status || got != r.want() {
			t.Errorf("%s: got %d %s, want %d %s", r.name, status, got, r.status, r.want())
		}
		if a, b := s.balance("bank-a"), s.balance("bank-b"); a != r.a() || b != r.b() {
			t.Errorf("%s: balances %d and %d, want %d and %d", r.name, a, b, r.a(), r.b())
		}
	}
	if !sessionText.MatchString(sid) {
		t.Errorf("the session ID %q does not have 30 digits", sid)
	}
	if n := s.open("bank-a"); n != 0 {
		t.Errorf("bank A still holds %d open sends", n)
	}
}

func TestSoloSendSurvivesRestarts(t *testing.T) {
	s := startSoloSend(t, 3*time.Second)
	a0, b0 := s.balance("bank-a"), s.balance("bank-b")
	debit := int64(soloSendAmount) + s.fees
	pending := func(outcome, key string) string {
		s.fault("bank-b", fmt.Sprintf(`{"kind":"timeout","outcome":%q,"count":1}`, outcome))
		status, _, got := s.send(key, soloSendAmount)
		if status != http.StatusOK || got["status"] != "pending" {
			t.Fatalf("%s: %d %v", key, status, got)
		}
		return fmt.Sprint(got["session_id"])
	}
	check := func(name string, a, b int64) {
		t.Helper()
		if gotA, gotB := s.balance("bank-a"), s.balance("bank-b"); gotA != a || gotB != b {
			t.Fatalf("%s: balances %d and %d, want %d and %d", name, gotA, gotB, a, b)
		}
	}

	credited := pending("credited", "restart-credited")
	s.members["bank-b"].stop()
	for range 4 {
		if status, got := s.query(credited); status != http.StatusOK || got["status"] != "pending" {
			t.Fatalf("a status query with bank B stopped: %d %v", status, got)
		}
	}
	s.startBank("bank-b")
	if status, got := s.query(credited); status != http.StatusOK || got["status"] != "00" || got["outcome"] != "credited" {
		t.Fatalf("a status query after bank B came back: %d %v", status, got)
	}
	check("credited once", a0-debit, b0+soloSendAmount)

	refused := pending("failed", "restart-failed")
	s.members["bank-b"].stop()
	if status, got := s.query(refused); status != http.StatusOK || got["status"] != "pending" {
		t.Fatalf("a status query with bank B stopped: %d %v", status, got)
	}
	s.startBank("bank-b")
	for range 2 {
		if status, got := s.query(refused); status != http.StatusOK || got["status"] != "25" || got["outcome"] != "failed" {
			t.Fatalf("a status query after bank B came back: %d %v", status, got)
		}
	}
	check("reversed once", a0-debit, b0+soloSendAmount)

	stuck := "000014261115120000000000000077"
	request := s.transferBody(stuck, soloSendAmount, true)
	a := s.members["bank-a"]
	if _, _, err := a.books.SendNIP(context.Background(), bank.NIPSend{Key: "crash-1", From: a.nuban, Amount: soloSendAmount, SessionID: stuck, To: bank.Counterparty{Name: "ADA OBI", AccountNumber: s.members["bank-b"].nuban, BankCode: "000015", BankName: "Bank B"}, At: time.Now(), NoCharges: true, Request: []byte(request), CheckAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	waitForWithin(t, soloRestartFor, "bank A to reverse a send that NIBSS refuses to take", func() bool { return s.open("bank-a") == 0 })
	check("a refused resend is reversed once", a0-debit, b0+soloSendAmount)

	resumed := pending("credited", "restart-a")
	s.members["bank-a"].stop()
	s.startBank("bank-a")
	waitForWithin(t, soloRestartFor, "bank A to finish the send after its restart", func() bool { return s.open("bank-a") == 0 })
	check("resumed once", a0-2*debit, b0+2*soloSendAmount)
	if status, got := s.query(resumed); status != http.StatusOK || got["status"] != "00" {
		t.Fatalf("the resumed session: %d %v", status, got)
	}
}

func waitForWithin(t *testing.T, limit time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
