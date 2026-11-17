package server

import (
	"slices"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/clock"
)

func fundedBank(t *testing.T) (*bank.Bank, string) {
	t.Helper()
	ctx := t.Context()
	at := time.Date(2026, 11, 4, 9, 0, 0, 0, clock.Lagos)
	b := reportBank(t)
	if err := b.Genesis(ctx, bank.GenesisSettlement, 200000000000, at); err != nil {
		t.Fatal(err)
	}
	c, err := b.CreateCustomer(ctx, "c0", "Ada Obi", "+2348030000001", clock.Date{Y: 1990, M: 1, D: 1}, "22912345678", "", "", at)
	if err != nil {
		t.Fatal(err)
	}
	acc, err := b.OpenAccount(ctx, "a0", c.ID, bank.ProductCurrent, at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.CashDeposit(ctx, "open:"+acc.NUBAN, acc.NUBAN, 50000000, at); err != nil {
		t.Fatal(err)
	}
	return b, acc.NUBAN
}

func TestSettlementProblems(t *testing.T) {
	ctx := t.Context()
	at := time.Date(2026, 11, 4, 12, 0, 0, 0, clock.Lagos)
	from := bank.Counterparty{Name: "CHIDI OKAFOR", AccountNumber: "0123456789", BankCode: "999002", BankName: "BANK B"}
	send := func(t *testing.T, b *bank.Bank, nuban string) {
		sid := "999001261104090000000000000001"
		if _, _, err := b.SendNIP(ctx, bank.NIPSend{Key: "send-1", From: nuban, Amount: 100, SessionID: sid, To: from, At: at, Request: []byte(`{"session_id":"` + sid + `"}`), CheckAt: at.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	sendDone := func(t *testing.T, b *bank.Bank, nuban string) {
		send(t, b, nuban)
		if err := b.FinishSend(ctx, "999001261104090000000000000001"); err != nil {
			t.Fatal(err)
		}
	}
	credit := func(t *testing.T, b *bank.Bank, nuban string) {
		if _, err := b.Credit(ctx, bank.NIPCredit{SessionID: "999002261104090000000000000001", FromCode: from.BankCode, FromBankName: from.BankName, FromNUBAN: from.AccountNumber, FromName: from.Name, ToNUBAN: nuban, Amount: 400, CreatedAt: at}, at); err != nil {
			t.Fatal(err)
		}
	}
	const funded = bank.GenesisSettlement
	paid := []SettlementReport{report("k1", 100, 0)}
	steps := []struct {
		name     string
		funded   bool
		setup    func(t *testing.T, b *bank.Bank, nuban string)
		during   func(t *testing.T, b *bank.Bank, nuban string)
		reports  []SettlementReport
		position settlementPosition
		cbn      cbnDesk
		after    string
		want     []string
	}{
		{name: "books agree", want: []string{}},
		{
			name:    "payable off with no open sends",
			setup:   func(t *testing.T, b *bank.Bank, _ string) { settle(t, b, "k1", 100, 0, at) },
			reports: paid, cbn: cbnDesk{balance: -100, postings: []cbnPosting{{Kind: "session", SessionID: "k1", Nets: []cbnNet{{BankID: "bank-a", Net: -100}}}}},
			after: "k1",
			want:  []string{"bank-a has NIP payable -100 and receivable 0, but its open NIBSS sessions come to 0 and 0"},
		},
		{name: "payable off while a send is open", funded: true, setup: send, cbn: cbnDesk{balance: funded}, want: []string{}},
		{name: "receivable within the pending range", funded: true, setup: credit, position: settlementPosition{Inward: 100, PendingInward: 300}, cbn: cbnDesk{balance: funded}, want: []string{}},
		{
			name: "receivable below the range", funded: true, setup: credit, position: settlementPosition{Inward: 500}, cbn: cbnDesk{balance: funded},
			want: []string{"bank-a has NIP payable 0 and receivable 400, but its open NIBSS sessions come to 0 and 500"},
		},
		{
			name: "receivable above the range", funded: true, setup: credit, position: settlementPosition{Inward: 100, PendingInward: 200}, cbn: cbnDesk{balance: funded},
			want: []string{"bank-a has NIP payable 0 and receivable 400, but its open NIBSS sessions come to 0 and 100, with 200 inward still pending"},
		},
		{
			name: "pending range that overflows", funded: true, setup: credit, position: settlementPosition{Inward: 100, PendingInward: 1<<63 - 1}, cbn: cbnDesk{balance: funded}, want: []string{},
		},
		{
			name:     "held differs from the CBN with nothing to book",
			cbn:      cbnDesk{balance: 50, postings: []cbnPosting{{Kind: "session", SessionID: "k9", Nets: []cbnNet{{BankID: "bank-a", Net: 50}}}}},
			position: settlementPosition{},
			want: []string{
				"bank-a's books show 0 at the CBN, but the CBN shows 50",
				"session k9 at bank-a: the bank posted 0, but the CBN moved 50",
			},
		},
		{name: "a credit lands while NIBSS is read", funded: true, during: credit, cbn: cbnDesk{balance: funded}, want: []string{}},
		{name: "a send is made while NIBSS is read", funded: true, during: sendDone, cbn: cbnDesk{balance: funded}, want: []string{}},
		{
			name: "a settlement posts while NIBSS is read", funded: true,
			during: func(t *testing.T, b *bank.Bank, _ string) { settle(t, b, "k1", 100, 0, at) },
			cbn:    cbnDesk{balance: funded - 100}, want: []string{},
		},
		{
			name:     "held differs from the CBN with a settlement to book",
			cbn:      cbnDesk{balance: 50},
			position: settlementPosition{Settlements: 1},
			want:     []string{},
		},
	}
	for _, s := range steps {
		b, nuban := reportBank(t), ""
		if s.funded {
			b, nuban = fundedBank(t)
		}
		if s.setup != nil {
			s.setup(t, b, nuban)
		}
		desk := &nibssDesk{reports: s.reports, position: s.position}
		if s.during != nil {
			desk.onPosition = func() { s.during(t, b, nuban) }
		}
		got, err := settlementProblems(ctx, desk.client(), s.cbn.client(), b, "bank-a")
		if err != nil || !slices.Equal(got, s.want) || got == nil {
			t.Errorf("%s: %q, %v, want %q", s.name, got, err, s.want)
		}
		if desk.positionAfter != s.after {
			t.Errorf("%s: asked NIBSS for the position after %q, want %q", s.name, desk.positionAfter, s.after)
		}
	}
}

func settle(t *testing.T, b *bank.Bank, key string, outward, inward int64, at time.Time) {
	t.Helper()
	if _, err := b.Settle(t.Context(), key, outward, inward, at); err != nil {
		t.Fatal(err)
	}
}
