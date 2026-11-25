package contracttest

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/banktest"
)

const unknownSession = "000014261115120000000000000099"

func (s *solo) openSends(id string) int {
	s.t.Helper()
	var n int
	if err := s.banks[id].books.L.Pool().QueryRow(s.ctx, `SELECT count(*) FROM nip_sends`).Scan(&n); err != nil {
		s.t.Fatal(err)
	}
	return n
}

func TestSoloSend(t *testing.T) {
	t.Parallel()
	s := newSolo(t, true, 3*time.Second, "", "bank-a", "bank-b")
	s.register("bank-a", soloCap)
	s.register("bank-b", soloCap)
	a, b := s.banks["bank-a"], s.banks["bank-b"]
	fees := banktest.Fees(t).ForTransfer(soloAmount)
	debit := int64(soloAmount) + fees.Fee + fees.VAT + fees.StampDuty
	a0, b0 := s.balance("bank-a"), s.balance("bank-b")
	check := func(name string, wantA, wantB int64) {
		t.Helper()
		if gotA, gotB := s.balance("bank-a"), s.balance("bank-b"); gotA != wantA || gotB != wantB {
			t.Fatalf("%s: balances %d and %d, want %d and %d", name, gotA, gotB, wantA, wantB)
		}
	}
	pending := func(outcome, key string) string {
		t.Helper()
		s.fault("bank-b", fmt.Sprintf(`{"kind":"timeout","outcome":%q,"count":1}`, outcome))
		status, _, got := s.send(key, soloAmount)
		if status != http.StatusOK || got["status"] != "pending" {
			t.Fatalf("%s: %d %v", key, status, got)
		}
		return fmt.Sprint(got["session_id"])
	}
	transfers := s.dirURL + "/v1/transfers"
	direct := func(name, client, body string, status int, want string) soloRow {
		return soloRow{name: name, client: s.clients[client], method: "POST", url: transfers, body: body, status: status, want: want}
	}
	s.check([]soloRow{
		direct("a closure from an active bank", "bank-a", strings.Replace(s.sessionBody("000014261115120000000000000001", soloAmount), `"closure":false`, `"closure":true`, 1), 422, soloErr("closure_not_allowed", "Bank 000014 is active, so it cannot send a closure transfer. Close the bank first, and send it again when the bank is terminating.")),
		direct("a stranger", "bank-z", s.sessionBody("000014261115120000000000000002", soloAmount), 403, soloErr("forbidden", "bank-z may not send transfers. It is not a bank NIBSS knows.")),
		direct("another bank sending as bank A", "bank-b", s.sessionBody("000014261115120000000000000003", soloAmount), 403, soloErr("forbidden", "bank-b may not send transfers for bank 000014.")),
		{name: "a status query for a session NIBSS never saw", client: s.clients["bank-a"], method: "POST", url: transfers + "/" + unknownSession + "/status-queries", status: 404, want: soloErr("not_found", "NIBSS has no session "+unknownSession+". Send the transfer again with the same session_id.")},
	})
	check("refusals move nothing", a0, b0)

	s.fault("bank-b", `{"kind":"down"}`)
	status, _, got := s.call(s.clients["bank-a"], http.MethodPost, transfers, "", s.sessionBody("000014261115120000000000000004", soloAmount), nil)
	if status != 200 || !strings.Contains(got, `"status":"91"`) || !strings.Contains(got, `"outcome":"failed"`) {
		t.Errorf("bank B down at NIBSS: %d %s, want a final 91", status, got)
	}
	if status, _, got := s.bankCall("bank-b", http.MethodDelete, "/v1/faults", "", ""); status != 200 {
		t.Fatalf("clear faults: %d %v", status, got)
	}

	status, _, bad := s.bankCall("bank-a", http.MethodPost, "/v1/transfers", "send-short", fmt.Sprintf(`{"from":%q,"to_bank":"bank-b","to":"123456789","amount":"10"}`, a.nuban))
	if want := "That is not a valid account number, so nothing was debited."; status != 422 || fmt.Sprint(bad["error"].(map[string]any)["message"]) != want {
		t.Errorf("a short account number: %d %v", status, bad)
	}
	if status, _, got := s.bankCall("bank-a", http.MethodPost, "/v1/nip/sends/"+unknownSession+"/status-query", "", ""); status != 404 || fmt.Sprint(got["error"].(map[string]any)["message"]) != "This bank has no send with that session ID." {
		t.Errorf("a status query for a send the bank never made: %d %v", status, got)
	}
	check("nothing was debited", a0, b0)

	holder, err := a.books.CustomerByBVN(s.ctx, "22912345678")
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.books.OpenAccount(s.ctx, "acc-ada-2", holder.ID, bank.ProductCurrent, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ownBody := func(amount string) string {
		return fmt.Sprintf(`{"from":%q,"to":%q,"amount":%q,"narration":"Own"}`, a.nuban, second.NUBAN, amount)
	}
	_, h, first := s.bankCall("bank-a", http.MethodPost, "/v1/transfers", "own-1", ownBody("1000.00"))
	_, hAgain, again := s.bankCall("bank-a", http.MethodPost, "/v1/transfers", "own-1", ownBody("1000.00"))
	if h.Get("Idempotent-Replayed") != "" || hAgain.Get("Idempotent-Replayed") != "true" || fmt.Sprint(first) != fmt.Sprint(again) {
		t.Errorf("an own-account replay: %v %v\n%v\n%v", h, hAgain, first, again)
	}
	refused, _, overdraw := s.bankCall("bank-a", http.MethodPost, "/v1/transfers", "broke-1", ownBody("999999999.00"))
	statusAgain, hRefused, overdrawAgain := s.bankCall("bank-a", http.MethodPost, "/v1/transfers", "broke-1", ownBody("999999999.00"))
	if refused != 422 || statusAgain != 422 || hRefused.Get("Idempotent-Replayed") != "true" || fmt.Sprint(overdraw) != fmt.Sprint(overdrawAgain) {
		t.Errorf("an overdraw replay: %d %d %v %v %v", refused, statusAgain, hRefused, overdraw, overdrawAgain)
	}
	a0 -= 100_000
	check("only the first own-account transfer moved money", a0, b0)

	failed := pending("failed", "restart-failed")
	b.stop()
	if got := s.query(failed); got["status"] != "pending" {
		t.Fatalf("a status query with bank B stopped: %v", got)
	}
	s.start("bank-b")
	for range 2 {
		if got := s.query(failed); got["status"] != "25" || got["outcome"] != "failed" {
			t.Fatalf("a status query after bank B came back: %v", got)
		}
	}
	check("a failed credit is reversed once", a0, b0)

	stuck := "000014261115120000000000000077"
	at := time.Now()
	if _, _, err := a.books.SendNIP(s.ctx, bank.NIPSend{Key: "crash-1", From: a.nuban, Amount: soloAmount, SessionID: stuck, To: bank.Counterparty{Name: "ADA OBI", AccountNumber: b.nuban, BankCode: "000015", BankName: "Bank B"}, At: at, NoCharges: true, Request: []byte(strings.Replace(s.sessionBody(stuck, soloAmount), `"closure":false`, `"closure":true`, 1)), CheckAt: at}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, waitLimit, "bank A to reverse a send that NIBSS refuses to take", func() bool { return s.openSends("bank-a") == 0 })
	check("a refused resend is reversed once", a0, b0)

	resumed := pending("credited", "restart-a")
	a.stop()
	s.start("bank-a")
	waitFor(t, waitLimit, "bank A to finish the send after its restart", func() bool { return s.openSends("bank-a") == 0 })
	check("a send resumed after a restart lands once", a0-debit, b0+soloAmount)
	if got := s.query(resumed); got["status"] != "00" {
		t.Fatalf("the resumed session: %v", got)
	}
}
