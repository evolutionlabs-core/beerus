package connect

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

type apiReply struct {
	status int
	raw    []byte
	body   map[string]any
}

func controlCall(t *testing.T, a *App, method, path, key, body string) apiReply {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, a.cfg.ControlURL()+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+a.link.Token)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := a.link.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	out := apiReply{status: resp.StatusCode, raw: raw}
	if err := json.Unmarshal(raw, &out.body); err != nil {
		t.Fatalf("reply %d is not JSON: %s", resp.StatusCode, raw)
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
	books := a.booksOf(bankID)
	if books == nil {
		t.Fatalf("the books of %s are not open", bankID)
	}
	dob := clock.Date{Y: 1990, M: 4, D: 21}
	c, err := books.createCustomer(t.Context(), "cus-"+name, Customer{Name: name, Phone: "08031234567", DOB: dob})
	if err != nil {
		t.Fatal(err)
	}
	acc, err := books.openAccount(t.Context(), "acc-"+name, c.ID, "current")
	if err != nil {
		t.Fatal(err)
	}
	if naira == "" {
		return acc.NUBAN
	}
	admin, err := a.adminOf(bankID)
	if err != nil {
		t.Fatal(err)
	}
	if err := admin.call(t.Context(), http.MethodPost, "/v1/deposits", "cash-"+name, server.CashRequest{Account: acc.NUBAN, Amount: naira}, nil); err != nil {
		t.Fatal(err)
	}
	a.SyncAll(t.Context())
	return acc.NUBAN
}

func confirmLimits(t *testing.T, a *App, id string) {
	t.Helper()
	ctx, stop := context.WithTimeout(t.Context(), testProvisionLimit)
	defer stop()
	tick := time.NewTicker(testProvisionPoll)
	defer tick.Stop()
	for a.staleMember(id) {
		select {
		case <-ctx.Done():
			t.Fatalf("the limits of %s never reached NIBSS", id)
		case <-tick.C:
		}
		a.reconcileNow(ctx)
	}
}

func refuseInwardCredits(t *testing.T, a *App, bankID string) {
	t.Helper()
	st, err := store.Open(t.Context(), a.cfg.DatabaseURL, a.cfg.Bank(bankID).Database)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := ledger.New(st.Pool, nil).SetStatus(t.Context(), bank.NIPInward, "frozen"); err != nil {
		t.Fatal(err)
	}
}

func balanceOf(t *testing.T, a *App, bankID, nuban string) int64 {
	t.Helper()
	acc, err := a.booksOf(bankID).account(t.Context(), nuban)
	if err != nil {
		t.Fatal(err)
	}
	return acc.Balance
}

func TestBankingThroughTheApp(t *testing.T) {
	a := newTestStack(t, Options{})
	from := funded(t, a, "bank-a", "Ada Obi", "50000")
	to := funded(t, a, "bank-b", "Ada Obi", "")
	booksA, booksB := a.booksOf("bank-a"), a.booksOf("bank-b")
	dob := clock.Date{Y: 1990, M: 4, D: 21}
	ca, err := booksA.createCustomer(t.Context(), "cus-twin-a", Customer{Name: "Ada Obi", Phone: "08031234567", DOB: dob})
	if err != nil {
		t.Fatal(err)
	}
	cb, err := booksB.createCustomer(t.Context(), "cus-twin-b", Customer{Name: "Ada Obi", Phone: "+2348031234567", DOB: dob})
	if err != nil || ca.BVN == "" || ca.BVN != cb.BVN {
		t.Fatalf("one person got BVNs %q and %q: %v", ca.BVN, cb.BVN, err)
	}
	if cs, err := booksA.createCustomer(t.Context(), "cus-twin-spaced", Customer{Name: "Ada Obi", Phone: "0803 123 4567", DOB: dob}); err != nil || cs.BVN != ca.BVN {
		t.Fatalf("a spaced phone gave BVN %q, want %q: %v", cs.BVN, ca.BVN, err)
	}
	badDOB := controlCall(t, a, http.MethodPost, "/banks/bank-a/customers", "cus-date", `{"name":"Ada Obi","phone":"08031234567","dob":"21/04/1990"}`)
	if badDOB.status != http.StatusUnprocessableEntity || errMessage(badDOB) != `invalid_request: Dob "21/04/1990" must be a date like 1990-04-21.` {
		t.Fatalf("a bad date of birth: %d %s", badDOB.status, errMessage(badDOB))
	}
	bad := controlCall(t, a, http.MethodPost, "/banks/bank-a/customers", "cus-phone", `{"name":"Ada Obi","phone":"0803-ABC","dob":"1990-04-21"}`)
	if bad.status != http.StatusUnprocessableEntity || !strings.HasPrefix(errMessage(bad), `invalid_request: Phone "0803-ABC" may hold only digits`) {
		t.Fatalf("a bad phone: %d %s", bad.status, errMessage(bad))
	}
	if r := controlCall(t, a, http.MethodPost, "/banks/bank-z/customers", "cus-z", `{"name":"Ada Obi","phone":"08031234567","dob":"1990-04-21"}`); r.status != http.StatusNotFound || errMessage(r) != "not_found: No such bank." {
		t.Fatalf("an unknown bank: %d %s", r.status, errMessage(r))
	}

	body := `{"from":"` + from + `","to_bank":"bank-b","to":"` + to + `","amount":"10000"}`
	var q control.TransferResult
	quoted := controlCall(t, a, http.MethodPost, "/banks/bank-a/transfers/quote", "", body)
	if err := json.Unmarshal(quoted.raw, &q); err != nil || quoted.status != http.StatusOK {
		t.Fatalf("quote %d %s: %v", quoted.status, quoted.raw, err)
	}
	if q.ToName != "Ada Obi" || q.Fee != 1000 || q.VAT != 75 || q.StampDuty != 5000 || q.TotalDebit != 1_006_075 || q.BalanceAfter != 3_993_925 || q.SessionID != "" {
		t.Fatalf("quote %+v", q)
	}

	sent := controlCall(t, a, http.MethodPost, "/banks/bank-a/transfers", "pay-1", body)
	again := controlCall(t, a, http.MethodPost, "/banks/bank-a/transfers", "pay-1", body)
	if sent.status != http.StatusOK || sent.body["status"] != "success" || sent.body["session_id"] != again.body["session_id"] {
		t.Fatalf("transfer %d %v, replay %v", sent.status, sent.body, again.body)
	}
	if sent.body["total_debit_minor"] != float64(q.TotalDebit) || sent.body["sender_balance_after_minor"] != float64(q.BalanceAfter) {
		t.Fatalf("the quote said %+v, the transfer did %v", q, sent.body)
	}
	reused := controlCall(t, a, http.MethodPost, "/banks/bank-a/transfers", "pay-1", strings.Replace(body, "10000", "9000", 1))
	if reused.status != http.StatusUnprocessableEntity || !strings.HasPrefix(errMessage(reused), "idempotency_key_reused:") {
		t.Fatalf("a reused key: %d %s", reused.status, errMessage(reused))
	}
	broke := controlCall(t, a, http.MethodPost, "/banks/bank-a/transfers", "pay-broke", strings.Replace(body, "10000", "1000000", 1))
	if broke.status != http.StatusUnprocessableEntity || errMessage(broke) != "insufficient_funds: Account "+from+" does not have enough money for this transfer and its charges." {
		t.Fatalf("an overdraw: %d %s", broke.status, errMessage(broke))
	}

	for key, c := range map[string][2]string{"bad-narration-long": {strings.Repeat("x", 101), "Narration has 101 characters; the limit is 100."}, "bad-narration-control": {"a\nb", "Narration must not contain control characters."}} {
		narration, want := c[0], c[1]
		_, err := a.transfer(t.Context(), control.TransferRequest{FromBank: "bank-a", From: from, ToBank: "bank-b", To: to, Amount: 100, Narration: narration, Key: key})
		var te *control.TransferError
		if !errors.As(err, &te) || te.Status != http.StatusUnprocessableEntity || te.Err.Error() != want {
			t.Fatalf("narration %q: got %v, want 422 %q", narration, err, want)
		}
	}
	direct := control.TransferRequest{FromBank: "bank-a", From: from, ToBank: "bank-b", To: to, Amount: 100_000, Key: "direct-1"}
	first, err := a.transfer(t.Context(), direct)
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.transfer(t.Context(), direct)
	if err != nil || second.SessionID != first.SessionID || second.Status != "success" || second.BalanceAfter != first.BalanceAfter || !second.At.Equal(first.At) {
		t.Fatalf("a repeat with the same key gave %+v, %v; the first was %+v", second, err, first)
	}
	if bal := balanceOf(t, a, "bank-a", from); bal != first.BalanceAfter {
		t.Fatalf("the sender holds %d after a repeated key, want one debit leaving %d", bal, first.BalanceAfter)
	}

	before, err := a.position(t.Context(), a.cfg.Bank("bank-a"), a.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.AddFault(t.Context(), control.FaultRequest{Kind: control.FaultTimeout, Bank: "bank-b", Outcome: nip.OutcomeCredited}); err != nil {
		t.Fatal(err)
	}
	held, err := a.transfer(t.Context(), control.TransferRequest{FromBank: "bank-a", From: from, ToBank: "bank-b", To: to, Amount: 200_000, Key: "held-1"})
	if err != nil || held.Status != "pending" {
		t.Fatalf("a held transfer: %+v, %v", held, err)
	}
	after, err := a.position(t.Context(), a.cfg.Bank("bank-a"), a.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if after["open_outward_minor"].(int64)-before["open_outward_minor"].(int64) != 200_000 {
		t.Fatalf("the pending transfer moved the open outward from %v to %v, want 200000 more", before["open_outward_minor"], after["open_outward_minor"])
	}
	if err := a.SetNetDebitCap(context.Background(), "bank-a", after["position_minor"].(int64), 0); err != nil {
		t.Fatal(err)
	}
	confirmLimits(t, a, "bank-a")
	capped, err := a.transfer(t.Context(), control.TransferRequest{FromBank: "bank-a", From: from, ToBank: "bank-b", To: to, Amount: 1, Key: "capped-1"})
	if err != nil || capped.Status != "reversed" || capped.ResponseCode != "61" || capped.BalanceAfter != held.BalanceAfter {
		t.Fatalf("a transfer past the cap with a pending session: %+v, %v; the held transfer left %d", capped, err, held.BalanceAfter)
	}

	if _, err := a.AddFault(t.Context(), control.FaultRequest{Kind: control.FaultTimeout, Bank: "bank-b", Outcome: nip.OutcomeCredited}); err != nil {
		t.Fatal(err)
	}
	refuseInwardCredits(t, a, "bank-b")
	if err := a.SetNetDebitCap(context.Background(), "bank-a", 0, 0); err != nil {
		t.Fatal(err)
	}
	confirmLimits(t, a, "bank-a")
	lost, err := a.transfer(t.Context(), control.TransferRequest{FromBank: "bank-a", From: from, ToBank: "bank-b", To: to, Amount: 300_000, Key: "lost-1"})
	if err != nil || lost.Status != "pending" {
		t.Fatalf("a held credit that failed: %+v, %v", lost, err)
	}
	if x, err := a.QuerySession(t.Context(), lost.SessionID); err != nil || x.Status != "25" || x.Outcome != nip.OutcomeFailed {
		t.Fatalf("the failed credit left session %+v, %v", x, err)
	}
	if bal := balanceOf(t, a, "bank-a", from); bal != capped.BalanceAfter {
		t.Fatalf("the sender holds %d after the failed credit was reversed, want %d", bal, capped.BalanceAfter)
	}
}

func TestBankChangesWaitForNIBSS(t *testing.T) {
	a, _ := newTestAppWithNIBSS(t, Options{})
	a.recordBank("bank-b", func(r *setup.Resource) bool {
		r.Phase = PhaseActive
		return true
	})
	setBankPhase(a, "bank-b", PhaseActive)
	liveBase := a.nibssCtl.Base
	outages := []struct {
		changes []bool
		status  string
		phase   string
	}{
		{[]bool{true}, nip.StatusSuspended, PhaseSuspended},
		{[]bool{false, true, false}, nip.StatusActive, PhaseActive},
	}
	for _, o := range outages {
		a.nibssCtl.Base = "http://127.0.0.1:1"
		for _, suspended := range o.changes {
			if err := a.SetBankSuspended(t.Context(), "bank-b", suspended); err != nil {
				t.Fatal(err)
			}
		}
		waiting, err := a.Resource(setup.KindBank, "bank-b")
		degraded := slices.ContainsFunc(waiting.Conditions, func(c setup.Condition) bool { return c.Type == setup.ConditionDegraded && c.Status == "True" })
		if err != nil || waiting.ObservedGeneration >= waiting.Generation || !degraded {
			t.Fatalf("%v with NIBSS down was observed: generation %d/%d, degraded %v, %v", o.changes, waiting.ObservedGeneration, waiting.Generation, degraded, err)
		}
		a.nibssCtl.Base = liveBase
		pollCtx, stop := context.WithTimeout(t.Context(), testProvisionLimit)
		tick := time.NewTicker(testProvisionPoll)
		for settled := false; !settled; {
			select {
			case <-pollCtx.Done():
				t.Fatalf("%v was never confirmed at NIBSS", o.changes)
			case <-tick.C:
			}
			a.reconcileNow(pollCtx)
			r, err := a.Resource(setup.KindBank, "bank-b")
			settled = err == nil && !a.staleMember("bank-b") && r.ObservedGeneration == r.Generation
		}
		tick.Stop()
		stop()
		done, err := a.Resource(setup.KindBank, "bank-b")
		degraded = slices.ContainsFunc(done.Conditions, func(c setup.Condition) bool { return c.Type == setup.ConditionDegraded && c.Status == "True" })
		if err != nil || done.ObservedGeneration != done.Generation || degraded || done.Phase != o.phase {
			t.Fatalf("%v after NIBSS answered: phase %s, generation %d/%d, degraded %v, %v", o.changes, done.Phase, done.ObservedGeneration, done.Generation, degraded, err)
		}
		if in, err := a.nibssCtl.member(t.Context(), "bank-b"); err != nil || in.Status != o.status {
			t.Fatalf("NIBSS shows bank-b as %+v after %v, want %s: %v", in, o.changes, o.status, err)
		}
	}
}
