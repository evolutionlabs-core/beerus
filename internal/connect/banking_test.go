package connect

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
)

type apiReply struct {
	status int
	body   map[string]any
}

func callHandler(t *testing.T, h http.HandlerFunc, bankID, body, key string) apiReply {
	t.Helper()
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))
	r.SetPathValue("id", bankID)
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	h(w, r)
	out := apiReply{status: w.Code}
	if err := json.Unmarshal(w.Body.Bytes(), &out.body); err != nil {
		t.Fatalf("reply %d is not JSON: %s", w.Code, w.Body)
	}
	return out
}

func errMessage(r apiReply) string {
	e, _ := r.body["error"].(map[string]any)
	return fmtAny(e["code"]) + ": " + fmtAny(e["message"])
}

func fmtAny(v any) string {
	s, _ := v.(string)
	return s
}

func funded(t *testing.T, a *App, bankID, name, naira string) string {
	t.Helper()
	c, err := a.CreateCustomer(bankID, CustomerRequest{Name: name, Phone: "08031234567", DOB: "1990-04-21"})
	if err != nil {
		t.Fatal(err)
	}
	acc, err := a.OpenAccount(bankID, AccountRequest{Customer: c.ID, Type: "current"})
	if err != nil {
		t.Fatal(err)
	}
	if naira == "" {
		return acc.NUBAN
	}
	if _, err := a.Cash(t.Context(), bankID, "deposit", CashRequest{Account: acc.NUBAN, Amount: naira}); err != nil {
		t.Fatal(err)
	}
	return acc.NUBAN
}

func TestBankingThroughTheApp(t *testing.T) {
	a := newTestApp(t, Options{})
	from := funded(t, a, "bank-a", "Ada Obi", "50000")
	to := funded(t, a, "bank-b", "Ada Obi", "")
	ca, _ := a.CreateCustomer("bank-a", CustomerRequest{Name: "Ada Obi", Phone: "08031234567", DOB: "1990-04-21"})
	cb, err := a.CreateCustomer("bank-b", CustomerRequest{Name: "Ada Obi", Phone: "+2348031234567", DOB: "1990-04-21"})
	if err != nil || ca.BVN == "" || ca.BVN != cb.BVN {
		t.Fatalf("one person got BVNs %q and %q: %v", ca.BVN, cb.BVN, err)
	}
	if cs, err := a.CreateCustomer("bank-a", CustomerRequest{Name: "Ada Obi", Phone: "0803 123 4567", DOB: "1990-04-21"}); err != nil || cs.BVN != ca.BVN {
		t.Fatalf("a spaced phone gave BVN %q, want %q: %v", cs.BVN, ca.BVN, err)
	}
	if _, err := a.CreateCustomer("bank-a", CustomerRequest{Name: "Ada Obi", Phone: "08031234567", DOB: "21/04/1990"}); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatalf("a bad date of birth: %v", err)
	}
	bad := callHandler(t, a.ctlCreateCustomer, "bank-a", `{"name":"Ada Obi","phone":"0803-ABC","dob":"1990-04-21"}`, "")
	if bad.status != http.StatusUnprocessableEntity || !strings.HasPrefix(errMessage(bad), `invalid_request: Phone "0803-ABC" may hold only digits`) {
		t.Fatalf("a bad phone: %d %s", bad.status, errMessage(bad))
	}
	if r := callHandler(t, a.ctlCreateCustomer, "bank-z", `{"name":"Ada Obi","phone":"08031234567","dob":"1990-04-21"}`, ""); r.status != http.StatusNotFound || errMessage(r) != "not_found: No such bank." {
		t.Fatalf("an unknown bank: %d %s", r.status, errMessage(r))
	}

	q, err := a.Quote(TransferRequest{FromBank: "bank-a", From: from, ToBank: "bank-b", To: to, Amount: 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if q.ToName != "Ada Obi" || q.Fee != 1000 || q.VAT != 75 || q.StampDuty != 5000 || q.TotalDebit != 1_006_075 || q.BalanceAfter != 3_993_925 || q.SessionID != "" {
		t.Fatalf("quote %+v", q)
	}

	body := `{"from":"` + from + `","to_bank":"bank-b","to":"` + to + `","amount":"10000"}`
	sent := callHandler(t, a.ctlBankTransfer, "bank-a", body, "pay-1")
	again := callHandler(t, a.ctlBankTransfer, "bank-a", body, "pay-1")
	if sent.status != http.StatusOK || sent.body["status"] != "success" || sent.body["session_id"] != again.body["session_id"] {
		t.Fatalf("transfer %d %v, replay %v", sent.status, sent.body, again.body)
	}
	if sent.body["total_debit_minor"] != float64(q.TotalDebit) || sent.body["sender_balance_after_minor"] != float64(q.BalanceAfter) {
		t.Fatalf("the quote said %+v, the transfer did %v", q, sent.body)
	}
	reused := callHandler(t, a.ctlBankTransfer, "bank-a", strings.Replace(body, "10000", "9000", 1), "pay-1")
	if reused.status != http.StatusUnprocessableEntity || !strings.HasPrefix(errMessage(reused), "idempotency_key_reused:") {
		t.Fatalf("a reused key: %d %s", reused.status, errMessage(reused))
	}
	broke := callHandler(t, a.ctlBankTransfer, "bank-a", strings.Replace(body, "10000", "1000000", 1), "")
	if broke.status != http.StatusUnprocessableEntity || errMessage(broke) != "insufficient_funds: Account "+from+" does not have enough money for this transfer and its charges." {
		t.Fatalf("an overdraw: %d %s", broke.status, errMessage(broke))
	}

	for narration, want := range map[string]string{strings.Repeat("x", 101): "narration has 101 characters; the limit is 100", "a\nb": "narration must not contain control characters"} {
		_, err := a.Transfer(TransferRequest{FromBank: "bank-a", From: from, ToBank: "bank-b", To: to, Amount: 100, Narration: narration, Key: "bad-narration"})
		var te *transferError
		if !errors.As(err, &te) || te.status != http.StatusUnprocessableEntity || te.err.Error() != want {
			t.Fatalf("narration %q: got %v, want 422 %q", narration, err, want)
		}
	}
	direct := TransferRequest{FromBank: "bank-a", From: from, ToBank: "bank-b", To: to, Amount: 100_000, Key: "direct-1"}
	first, err := a.Transfer(direct)
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.Transfer(direct)
	if err != nil || second.SessionID != first.SessionID || second.Status != "success" || second.BalanceAfter != first.BalanceAfter || !second.At.Equal(first.At) {
		t.Fatalf("a repeat with the same key gave %+v, %v; the first was %+v", second, err, first)
	}
	bk, err := a.bankBook("bank-a")
	if err != nil {
		t.Fatal(err)
	}
	if bal, err := bk.ls.Bank().BalanceAt(context.Background(), from, a.clock.Now()); err != nil || bal != first.BalanceAfter {
		t.Fatalf("the sender holds %d after a repeated key, want one debit leaving %d: %v", bal, first.BalanceAfter, err)
	}

	before, err := a.position(a.cfg.Bank("bank-a"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.AddFault(FaultRequest{Kind: FaultTimeout, Bank: "bank-b", Outcome: nibss.OutcomeCredited}); err != nil {
		t.Fatal(err)
	}
	held, err := a.Transfer(TransferRequest{FromBank: "bank-a", From: from, ToBank: "bank-b", To: to, Amount: 200_000, Key: "held-1"})
	if err != nil || held.Status != "pending" {
		t.Fatalf("a held transfer: %+v, %v", held, err)
	}
	after, err := a.position(a.cfg.Bank("bank-a"))
	if err != nil {
		t.Fatal(err)
	}
	if after["open_outward_minor"].(int64)-before["open_outward_minor"].(int64) != 200_000 {
		t.Fatalf("the pending transfer moved the open outward from %v to %v, want 200000 more", before["open_outward_minor"], after["open_outward_minor"])
	}
	if err := a.SetNetDebitCap("bank-a", after["position_minor"].(int64), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Transfer(TransferRequest{FromBank: "bank-a", From: from, ToBank: "bank-b", To: to, Amount: 1}); !errors.Is(err, errOverCap) {
		t.Fatalf("a transfer past the cap with a pending session: %v", err)
	}

	if _, err := a.AddFault(FaultRequest{Kind: FaultTimeout, Bank: "bank-b", Outcome: nibss.OutcomeCredited}); err != nil {
		t.Fatal(err)
	}
	bkB, err := a.bankBook("bank-b")
	if err != nil {
		t.Fatal(err)
	}
	if err := bkB.ls.Bank().L.SetStatus(context.Background(), bank.NIPInward, "frozen"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetNetDebitCap("bank-a", 0, 0); err != nil {
		t.Fatal(err)
	}
	lost, err := a.Transfer(TransferRequest{FromBank: "bank-a", From: from, ToBank: "bank-b", To: to, Amount: 300_000, Key: "lost-1"})
	if err != nil || lost.Status != "reversed" {
		t.Fatalf("a held credit that failed: %+v, %v", lost, err)
	}
	if x, err := a.Session(lost.SessionID); err != nil || x.Outcome != nibss.OutcomeReversed {
		t.Fatalf("the failed credit left session %+v, %v", x, err)
	}

}
