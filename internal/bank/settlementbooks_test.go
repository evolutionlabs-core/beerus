package bank_test

import (
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
)

func TestSettlementBooks(t *testing.T) {
	ctx := t.Context()
	a := newBank(t, "bank-a", "999001", "901", 1)
	from := bank.Counterparty{Name: "CHIDI OKAFOR", AccountNumber: "0123456789", BankCode: "999002", BankName: "BANK B"}
	steps := []struct {
		name string
		run  func() error
		want bank.SettlementBooks
	}{
		{"before any settlement", func() error { return nil }, bank.SettlementBooks{Held: 1000000000000}},
		{"after a send", func() error {
			_, _, err := a.SendNIP(ctx, bank.NIPSend{Key: "send-1", From: a.custs[0], Amount: 10000000, SessionID: "999001260302090000000000000001", To: from, At: t0.Add(time.Hour)})
			return err
		}, bank.SettlementBooks{Payable: 10000000, Held: 1000000000000}},
		{"after an inward credit", func() error {
			_, err := credit(ctx, a.Bank, "999002260302090000000000000001", a.custs[0], 4000000, from, "", t0.Add(time.Hour))
			return err
		}, bank.SettlementBooks{Payable: 10000000, Receivable: 4000000, Held: 1000000000000}},
		{"after the settlement", func() error {
			_, err := a.Settle(ctx, "s1", 10000000, 4000000, t0.Add(2*time.Hour))
			return err
		}, bank.SettlementBooks{Held: 999994000000, Last: "s1"}},
		{"after a later settlement", func() error {
			_, err := a.Settle(ctx, "s2", 0, 1000, t0.Add(3*time.Hour))
			return err
		}, bank.SettlementBooks{Receivable: -1000, Held: 999994001000, Last: "s2"}},
	}
	for _, s := range steps {
		if err := s.run(); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		got, err := a.SettlementBooks(ctx)
		if err != nil || got != s.want {
			t.Errorf("%s: %+v, %v, want %+v", s.name, got, err, s.want)
		}
		if got.Payable != bal(t, a.Bank, bank.NIPOutward) || got.Receivable != bal(t, a.Bank, bank.NIPInward) || got.Held != bal(t, a.Bank, bank.Settlement) {
			t.Errorf("%s: %+v differs from the ledger balances", s.name, got)
		}
	}
}
