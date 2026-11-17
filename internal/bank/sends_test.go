package bank_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
)

func TestOpenSendsLiveAndDieWithTheirDebit(t *testing.T) {
	ctx := context.Background()
	a := newBank(t, "bank-a", "999001", "901", 1)
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
	if err := a.RescheduleSend(ctx, first, next); err != nil {
		t.Fatal(err)
	}
	row, open, err := a.OpenSend(ctx, first)
	if err != nil || !open || row.Attempts != 1 || !row.NextAt.Equal(next) || string(row.Request) != `{"session_id": "`+first+`"}` {
		t.Fatalf("after a reschedule: %+v %v %v", row, open, err)
	}
	if err := a.FinishSend(ctx, first); err != nil {
		t.Fatal(err)
	}
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
