package server

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/control"
)

var problemsAt = time.Date(2026, 11, 4, 12, 0, 0, 0, clock.Lagos)

const (
	firstSID  = "999001261104090000000000000001"
	creditSID = "999002261104090000000000000001"
)

func fundedBank(t *testing.T) (*bank.Bank, string) {
	t.Helper()
	ctx := t.Context()
	b := reportBank(t)
	chk(t, b.Genesis(ctx, control.GenesisSettlement, 200000000000, problemsAt))
	c, err := b.CreateCustomer(ctx, "c0", "Ada Obi", "+2348030000001", dob, "22912345678", "", "", problemsAt)
	chk(t, err)
	acc, err := b.OpenAccount(ctx, "a0", c.ID, bank.ProductCurrent, problemsAt)
	chk(t, err)
	_, err = b.CashDeposit(ctx, "open:"+acc.NUBAN, acc.NUBAN, 50000000, problemsAt)
	chk(t, err)
	return b, acc.NUBAN
}

func settle(t *testing.T, b *bank.Bank, key string, outward, inward int64) {
	t.Helper()
	_, err := b.Settle(t.Context(), key, outward, inward, problemsAt)
	chk(t, err)
}

func sendNIP(t *testing.T, b *bank.Bank, nuban string) {
	t.Helper()
	to := bank.Counterparty{Name: "CHIDI OKAFOR", AccountNumber: "0123456789", BankCode: "999002", BankName: "BANK B"}
	_, _, err := b.SendNIP(t.Context(), bank.NIPSend{Key: "send-1", From: nuban, Amount: 100, SessionID: firstSID, To: to, At: problemsAt, Request: []byte(`{"session_id":"` + firstSID + `"}`), CheckAt: problemsAt.Add(time.Hour)})
	chk(t, err)
	chk(t, b.FinishSend(t.Context(), firstSID))
}

func creditNIP(t *testing.T, b *bank.Bank, nuban string) {
	t.Helper()
	_, err := b.Credit(t.Context(), bank.NIPCredit{SessionID: creditSID, FromCode: "999002", FromBankName: "BANK B", FromNUBAN: "0123456789", FromName: "CHIDI OKAFOR", ToNUBAN: nuban, Amount: 400, CreatedAt: problemsAt}, problemsAt)
	chk(t, err)
}

func TestSettlementProblems(t *testing.T) {
	t.Parallel()
	pt := t
	const funded = control.GenesisSettlement
	paid := []SettlementReport{report("k1", 100, 0)}
	k1 := cbnDesk{balance: -100, postings: []cbnPosting{{Kind: "session", SessionID: "k1", Nets: []cbnNet{{BankID: "bank-a", Net: -100}}}}}
	k9 := cbnDesk{balance: 50, postings: []cbnPosting{{Kind: "session", SessionID: "k9", Nets: []cbnNet{{BankID: "bank-a", Net: 50}}}}}
	books := map[string]func(*testing.T, *bank.Bank, string){
		"empty":    nil,
		"settled":  func(t *testing.T, b *bank.Bank, _ string) { settle(t, b, "k1", 100, 0) },
		"credited": creditNIP,
	}
	banks := map[string]*bank.Bank{}
	nubans := map[string]string{}
	for _, s := range []struct {
		name, books, after string
		during             func(*testing.T, *bank.Bank, string)
		reports            []SettlementReport
		position           settlementPosition
		cbn                cbnDesk
		want               []string
	}{
		{name: "books agree", books: "empty", want: []string{}},
		{name: "payable off with no open sends", books: "settled", reports: paid, cbn: k1, after: "k1", want: []string{"bank-a has NIP payable -100 and receivable 0, but its open NIBSS sessions come to 0 and 0"}},
		{name: "receivable within the pending range", books: "credited", position: settlementPosition{Inward: 100, PendingInward: 300}, cbn: cbnDesk{balance: funded}, want: []string{}},
		{name: "receivable below the range", books: "credited", position: settlementPosition{Inward: 500}, cbn: cbnDesk{balance: funded}, want: []string{"bank-a has NIP payable 0 and receivable 400, but its open NIBSS sessions come to 0 and 500"}},
		{name: "receivable above the range", books: "credited", position: settlementPosition{Inward: 100, PendingInward: 200}, cbn: cbnDesk{balance: funded}, want: []string{"bank-a has NIP payable 0 and receivable 400, but its open NIBSS sessions come to 0 and 100, with 200 inward still pending"}},
		{name: "pending range that overflows", books: "credited", position: settlementPosition{Inward: 100, PendingInward: math.MaxInt64}, cbn: cbnDesk{balance: funded}, want: []string{}},
		{name: "held differs from the CBN with nothing to book", books: "empty", cbn: k9, want: []string{"bank-a's books show 0 at the CBN, but the CBN shows 50", "session k9 at bank-a: the bank posted 0, but the CBN moved 50"}},
		{name: "held differs with a settlement to book", books: "empty", position: settlementPosition{Settlements: 1}, cbn: cbnDesk{balance: 50}, want: []string{}},
		{name: "a send is made while NIBSS is read", books: "own", during: sendNIP, cbn: cbnDesk{balance: funded}, want: []string{}},
		{name: "a settlement posts while NIBSS is read", books: "own", during: func(t *testing.T, b *bank.Bank, _ string) { settle(t, b, "k1", 100, 0) }, cbn: cbnDesk{balance: funded - 100}, want: []string{}},
	} {
		t.Run(s.name, func(t *testing.T) {
			b, nuban := banks[s.books], nubans[s.books]
			if b == nil {
				b, nuban = reportBank(pt), ""
				if s.books == "credited" || s.books == "own" {
					b, nuban = fundedBank(pt)
				}
				if setup := books[s.books]; setup != nil {
					setup(pt, b, nuban)
				}
				if s.books != "own" {
					banks[s.books], nubans[s.books] = b, nuban
				}
			}
			desk := &nibssDesk{reports: s.reports, position: s.position}
			if s.during != nil {
				desk.onPosition = func() { s.during(t, b, nuban) }
			}
			got, err := settlementProblems(t.Context(), desk.client(), s.cbn.client(), b, "bank-a")
			if err != nil || !slices.Equal(got, s.want) || got == nil || desk.positionAfter != s.after {
				t.Errorf("%q after %q, %v; want %q after %q", got, desk.positionAfter, err, s.want, s.after)
			}
		})
	}
}
