package bank_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/clock"
)

type feedRow struct {
	amount  int64
	channel string
	party   string
	session bool
}

func feedRows(t *testing.T, b *bank.Bank, nuban string, at time.Time) []feedRow {
	t.Helper()
	lines, err := b.Feed(context.Background(), nuban, at, clock.DateOf(t0))
	if err != nil {
		t.Fatal(err)
	}
	out := []feedRow{}
	for _, l := range lines {
		r := feedRow{amount: l.Amount, channel: l.Channel, session: l.SessionID != ""}
		if l.Counterparty != nil {
			r.party = l.Counterparty.Name + "/" + l.Counterparty.AccountNumber + "/" + l.Counterparty.BankCode
		}
		if l.BankRef == "" || l.Key == "" {
			t.Fatalf("line %+v has no key or bank reference", l)
		}
		out = append(out, r)
	}
	return out
}

func sameRows(t *testing.T, got, want []feedRow) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d lines %+v, want %d %+v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestFeedComesFromPostings(t *testing.T) {
	ctx := context.Background()
	a := newBank(t, "bank-a", "999001", "901", 2)
	from, to := a.custs[0], a.custs[1]
	at := t0.Add(time.Hour)
	sid := "999001260302100000000000000001"
	sent, c, err := a.SendNIP(ctx, bank.NIPSend{Key: sid, From: from, Amount: 1000000, SessionID: sid, To: bank.Counterparty{Name: "CHIDI OKAFOR", AccountNumber: "9020000011", BankCode: "999002", BankName: "BANK B"}, At: at})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ReverseNIP(ctx, sent.EntryID, sid, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.OwnTransfer(ctx, "own-1", from, to, 5000, "", at.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	chidi := "CHIDI OKAFOR/9020000011/999002"
	other := "bank-a customer 1/" + to + "/999001"
	want := []feedRow{
		{amount: 50000000, channel: "branch"},
		{amount: -1000000, channel: "nip", party: chidi, session: true},
		{amount: -c.Fee, channel: "charge", session: true},
		{amount: -c.VAT, channel: "charge", session: true},
		{amount: -c.StampDuty, channel: "charge", session: true},
		{amount: 1000000, channel: "reversal", party: chidi, session: true},
		{amount: c.Fee, channel: "reversal", session: true},
		{amount: c.VAT, channel: "reversal", session: true},
		{amount: c.StampDuty, channel: "reversal", session: true},
		{amount: -5000, channel: "internal", party: other},
	}
	sameRows(t, feedRows(t, a.Bank, from, at.Add(time.Hour)), want)
	sameRows(t, feedRows(t, a.Bank, from, at), want[:5])
	sameRows(t, feedRows(t, a.Bank, to, at.Add(time.Hour)), []feedRow{
		{amount: 50000000, channel: "branch"},
		{amount: 5000, channel: "internal", party: "bank-a customer 0/" + from + "/999001"},
	})
	got, err := a.BalanceAt(ctx, from, at)
	if err != nil {
		t.Fatal(err)
	}
	if want := 50000000 - 1000000 - c.Fee - c.VAT - c.StampDuty; got != want {
		t.Fatalf("balance at the send: %d, want %d", got, want)
	}
	if got, err := a.BalanceAt(ctx, from, t0.Add(-time.Second)); err != nil || got != 0 {
		t.Fatalf("balance before opening: %d %v", got, err)
	}
	if _, err := a.BalanceAt(ctx, "9019999999", at); !errors.Is(err, ledger.ErrUnknownAccount) {
		t.Fatalf("balance of an unknown account: %v", err)
	}

	seen := []int64{}
	cursor := ""
	for page := 0; page < 4; page++ {
		lines, next, err := a.Recent(ctx, from, 4, cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range lines {
			seen = append(seen, l.Amount)
		}
		if cursor = next; cursor == "" {
			break
		}
	}
	if len(seen) != len(want) || seen[0] != want[len(want)-1].amount || seen[len(seen)-1] != want[0].amount {
		t.Fatalf("recent lines newest first: %v", seen)
	}
	if _, _, err := a.Recent(ctx, from, 4, "x"); !errors.Is(err, bank.ErrBadCursor) {
		t.Fatalf("a bad cursor: %v", err)
	}
	session, err := a.SessionLines(ctx, sid)
	if err != nil || len(session) != 8 {
		t.Fatalf("session lines: %d %v", len(session), err)
	}
	line, err := a.Line(ctx, from, session[0].Key)
	if err != nil || line.Amount != -1000000 || line.SessionID != sid || line.NUBAN != from {
		t.Fatalf("line %+v %v", line, err)
	}
	if _, err := a.Line(ctx, to, session[0].Key); !errors.Is(err, bank.ErrUnknownLine) {
		t.Fatalf("a line read through another account: %v", err)
	}
	tot, err := a.Totals(ctx)
	if err != nil || tot.Customers != 2 || tot.Accounts != 2 || tot.Deposits != 100000000 {
		t.Fatalf("totals %+v %v", tot, err)
	}
}

func TestReceivedNIPKeepsTheSender(t *testing.T) {
	ctx := context.Background()
	b := newBank(t, "bank-b", "999002", "902", 1)
	sid := "999001260302100000000000000002"
	sender := bank.Counterparty{Name: "ADAEZE OKAFOR", AccountNumber: "9010000014", BankCode: "999001", BankName: "BANK A"}
	if _, err := b.ReceiveNIP(ctx, sid, b.custs[0], 1000000, sender, "", false, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	sameRows(t, feedRows(t, b.Bank, b.custs[0], t0.Add(2*time.Hour)), []feedRow{
		{amount: 50000000, channel: "branch"},
		{amount: 1000000, channel: "nip", party: "ADAEZE OKAFOR/9010000014/999001", session: true},
	})
}
