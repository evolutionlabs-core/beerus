package bank_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/clock"
)

func reverseNIP(t *testing.T, a *testBank) {
	ctx := context.Background()
	from := a.custs[0]
	before := bal(t, a.Bank, bank.CustomerAccount(from))
	fee0, vat0, out0 := bal(t, a.Bank, bank.FeeIncome), bal(t, a.Bank, bank.VATPayable), bal(t, a.Bank, bank.NIPOutward)
	sid := "999001260302100000000000000042"
	sent, _, err := a.SendNIP(ctx, bank.NIPSend{Key: sid, From: from, Amount: 10000000, SessionID: sid, To: bank.Counterparty{Name: "CHIDI OKAFOR", BankName: "BANK B"}, At: t0.Add(time.Hour)})
	chk(t, err)
	_, err = a.ReverseNIP(ctx, sent.EntryID, "999001260302100000000000000043", t0.Add(2*time.Hour))
	wantErr(t, err, ledger.ErrInvalid, fmt.Sprintf("ledger: invalid entry: entry %d is not the NIP send for session 999001260302100000000000000043", sent.EntryID))
	dep, err := a.CashDeposit(ctx, "dep", from, 100, t0)
	chk(t, err)
	_, err = a.ReverseNIP(ctx, dep.EntryID, sid, t0.Add(2*time.Hour))
	wantErr(t, err, ledger.ErrInvalid, fmt.Sprintf("ledger: invalid entry: entry %d is not the NIP send for session %s", dep.EntryID, sid))
	rev, err := a.ReverseNIP(ctx, sent.EntryID, sid, t0.Add(2*time.Hour))
	chk(t, err)
	again, err := a.ReverseNIP(ctx, sent.EntryID, sid, t0.Add(3*time.Hour))
	if err != nil || !again.Replayed || again.EntryID != rev.EntryID {
		t.Fatalf("second reversal %+v %v", again, err)
	}
	if got := bal(t, a.Bank, bank.CustomerAccount(from)); got != before+100 {
		t.Fatalf("balance %d after reversal, want %d", got, before+100)
	}
	lines, _, err := a.Statement(ctx, from, clock.Date{}, clock.Date{}, 20, "")
	chk(t, err)
	var credits []ledger.Event
	for _, ln := range lines {
		if ln.EntryID == rev.EntryID {
			credits = append(credits, ln)
		}
	}
	want := []struct {
		amount    int64
		narration string
	}{
		{10000000, "REV/NIP TRF TO CHIDI OKAFOR/BANK B/000000000042"},
		{5000, "REV/NIP CHARGE 000000000042"},
		{375, "REV/VAT ON NIP CHARGE 000000000042"},
		{5000, "REV/STAMP DUTY 000000000042"},
	}
	if len(credits) != len(want) {
		t.Fatalf("reversal has %d customer lines, want %d: %+v", len(credits), len(want), credits)
	}
	for i, w := range want {
		if credits[i].Amount != w.amount || credits[i].Narration != w.narration || credits[i].SessionID != sid {
			t.Fatalf("reversal line %d is %+v, want %d %q for session %s", i, credits[i], w.amount, w.narration, sid)
		}
	}
	if bal(t, a.Bank, bank.FeeIncome) != fee0 || bal(t, a.Bank, bank.VATPayable) != vat0 || bal(t, a.Bank, bank.NIPOutward) != out0 {
		t.Fatal("reversal left fee, VAT or payable behind")
	}
	mustOK(t, a.L)
}

func openSends(t *testing.T, a *testBank) {
	ctx := context.Background()
	to := bank.Counterparty{Name: "CHIDI OKAFOR", BankName: "BANK B"}
	due := t0.Add(time.Hour)
	send := func(key, sid string, amount int64, check time.Time) error {
		_, _, err := a.SendNIP(ctx, bank.NIPSend{Key: key, From: a.custs[0], Amount: amount, SessionID: sid, To: to, At: t0, Request: []byte(`{"session_id":"` + sid + `"}`), CheckAt: check})
		return err
	}
	first, second, third := "999001260302090000000000000001", "999001260302090000000000000002", "999001260302090000000000000003"

	steps := []struct {
		name string
		run  func() error
		open []string
	}{
		{"a send leaves a row", func() error { return send("k1", first, 1000, due) }, []string{first}},
		{"a repeat adds no row", func() error { return send("k1", first, 1000, due) }, []string{first}},
		{"a refused debit leaves no row", func() error { return send("k2", second, 1<<50, due) }, []string{first}},
		{"a second send", func() error { return send("k3", third, 1000, due.Add(-time.Minute)) }, []string{third, first}},
		{"finishing a send removes its row", func() error { return a.FinishSend(ctx, third) }, []string{first}},
		{"a repeat of a finished send does not bring its row back", func() error { return send("k3", third, 1000, due) }, []string{first}},
	}
	for _, s := range steps {
		err := s.run()
		if s.name == "a refused debit leaves no row" {
			if !errors.Is(err, ledger.ErrInsufficientFunds) {
				t.Fatalf("%s: %v", s.name, err)
			}
		} else if err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		rows, err := a.DueSends(ctx, due.Add(24*time.Hour), 10)
		if err != nil || len(rows) != len(s.open) {
			t.Fatalf("%s: %d rows, %v; want %v", s.name, len(rows), err, s.open)
		}
		for i, r := range rows {
			if r.SessionID != s.open[i] {
				t.Fatalf("%s: row %d is %s, want %s", s.name, i, r.SessionID, s.open[i])
			}
		}
	}

	if rows, err := a.DueSends(ctx, due.Add(-time.Second), 10); err != nil || len(rows) != 0 {
		t.Fatalf("nothing is due before the check time: %v, %v", rows, err)
	}
	next := due.Add(time.Hour)
	chk(t, a.RescheduleSend(ctx, first, next))
	row, open, err := a.OpenSend(ctx, first)
	if err != nil || !open || row.Attempts != 1 || !row.NextAt.Equal(next) || string(row.Request) != `{"session_id": "`+first+`"}` {
		t.Fatalf("after a reschedule: %+v %v %v", row, open, err)
	}
	chk(t, a.FinishSend(ctx, first))
	if _, open, err := a.OpenSend(ctx, first); err != nil || open {
		t.Fatalf("a finished send is still open: %v %v", open, err)
	}
	one, err := a.NextSendSeq(ctx)
	two, err2 := a.NextSendSeq(ctx)
	if err != nil || err2 != nil || two != one+1 {
		t.Fatalf("send numbers %d and %d: %v %v", one, two, err, err2)
	}
	mustOK(t, a.L)
}

func settlementBooks(t *testing.T, a *testBank) {
	ctx := t.Context()
	from := bank.Counterparty{Name: "CHIDI OKAFOR", AccountNumber: "0123456789", BankCode: "999002", BankName: "BANK B"}
	base, err := a.SettlementBooks(ctx)
	chk(t, err)
	steps := []struct {
		name                       string
		run                        func() error
		payable, receivable, moved int64
		last                       string
	}{
		{"before any settlement", func() error { return nil }, 0, 0, 0, ""},
		{"after a send", func() error {
			_, _, err := a.SendNIP(ctx, bank.NIPSend{Key: "send-1", From: a.custs[0], Amount: 10000000, SessionID: "999001260302070000000000000001", To: from, At: t0.Add(time.Hour)})
			return err
		}, 10000000, 0, 0, ""},
		{"after an inward credit", func() error {
			_, err := credit(ctx, a.Bank, "999002260302070000000000000001", a.custs[0], 4000000, from, "", t0.Add(time.Hour))
			return err
		}, 10000000, 4000000, 0, ""},
		{"after the settlement", func() error {
			_, err := a.Settle(ctx, "s1", 10000000, 4000000, t0.Add(2*time.Hour))
			return err
		}, 0, 0, -6000000, "s1"},
		{"after a later settlement", func() error {
			_, err := a.Settle(ctx, "s2", 0, 1000, t0.Add(3*time.Hour))
			return err
		}, 0, -1000, -5999000, "s2"},
	}
	for _, s := range steps {
		if err := s.run(); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		want := bank.SettlementBooks{Payable: base.Payable + s.payable, Receivable: base.Receivable + s.receivable, Held: base.Held + s.moved, Last: s.last}
		got, err := a.SettlementBooks(ctx)
		if err != nil || got != want {
			t.Errorf("%s: %+v, %v, want %+v", s.name, got, err, want)
		}
		if got.Payable != bal(t, a.Bank, bank.NIPOutward) || got.Receivable != bal(t, a.Bank, bank.NIPInward) || got.Held != bal(t, a.Bank, bank.Settlement) {
			t.Errorf("%s: %+v differs from the ledger balances", s.name, got)
		}
	}
}

func limitsAndClosures(t *testing.T, a *testBank) {
	ctx := context.Background()
	c0, c1 := a.custs[0], a.custs[1]
	at := t0.Add(2 * time.Hour)
	p1, err := a.CashDeposit(ctx, "retry-1", c0, 100000, at)
	chk(t, err)
	p2, err := a.CashDeposit(ctx, "retry-1", c0, 100000, at.Add(time.Second))
	if err != nil || !p2.Replayed || p2.EntryID != p1.EntryID {
		t.Fatalf("a retry one second later was not a replay: %+v %v", p2, err)
	}
	sid := "999001260302110000000000000777"
	sent, _, err := a.SendNIP(ctx, bank.NIPSend{Key: "nip-rev:" + sid, From: c0, Amount: 2000000, SessionID: sid, To: bank.Counterparty{Name: "X", BankName: "BANK B"}, At: at})
	chk(t, err)
	chk(t, a.L.SetStatus(ctx, bank.CustomerAccount(c0), "frozen"))
	if _, err := a.ReverseNIP(ctx, sent.EntryID, sid, at.Add(time.Hour)); err != nil {
		t.Fatalf("refund to a frozen account, or a client key colliding with the reversal key: %v", err)
	}
	if _, err := credit(ctx, a.Bank, "123", c1, 100, bank.Counterparty{Name: "X", BankName: "Y"}, "", at); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatalf("short session ID: %v", err)
	}
	_, err = a.L.OpenAccount(ctx, ledger.Account{Code: bank.CustomerAccount("9020000027"), Name: "Tier one", Kind: ledger.Liability, Customer: true, NUBAN: "9020000027", Product: "savings", Owner: "tier1", KYCTier: 1})
	chk(t, err)
	_, err = a.CashDeposit(ctx, "t1dep", "9020000027", 20000000, at)
	chk(t, err)
	if _, err := a.CashWithdrawal(ctx, "t1wd", "9020000027", 10000000, at); !errors.Is(err, ledger.ErrDailyLimit) {
		t.Fatalf("tier 1 cash withdrawal over the daily limit: %v", err)
	}
	to := bank.Counterparty{Name: "X", BankName: "Y"}
	if _, _, err := a.SendNIP(ctx, bank.NIPSend{Key: "t1nip", From: "9020000027", Amount: 10000000, SessionID: "999001260302100000000000000071", To: to, At: at}); !errors.Is(err, ledger.ErrDailyLimit) {
		t.Fatalf("tier 1 NIP send over the daily limit: %v", err)
	}
	if _, _, err := a.SendNIP(ctx, bank.NIPSend{Key: "close:bank-a:9020000027", From: "9020000027", Amount: 10000000, SessionID: "999001260302100000000000000072", To: to, At: at, NoCharges: true}); err != nil {
		t.Fatalf("a closure send must bypass the daily limit: %v", err)
	}
	if _, err := a.ClosurePayout(ctx, "close:bank-a:9020000027", "9020000027", 1000000, at); err != nil {
		t.Fatalf("closure payout over the daily limit: %v", err)
	}
	if _, err := a.ClosurePayout(ctx, "close:bank-a:9020000027", "9020000027", 1000000, at); err != nil {
		t.Fatalf("a repeated closure payout: %v", err)
	}
	if paid, err := a.ClosurePayouts(ctx); err != nil || paid["9020000027"] != 1000000 {
		t.Fatalf("closure payouts %v %v, want one payout of 1000000", paid, err)
	}
	chk(t, a.FreezeAccount(ctx, "9020000027"))
	if status, err := a.AccountStatus(ctx, "9020000027"); err != nil || status != "frozen" {
		t.Fatalf("account status %q %v", status, err)
	}
	mustOK(t, a.L)
}
