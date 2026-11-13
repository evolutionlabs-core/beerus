package bank_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/banktest"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/charges"
	"github.com/evolutionlabs-core/beerus/internal/clock"
)

var t0 = time.Date(2026, 3, 2, 9, 0, 0, 0, clock.Lagos)

type testBank struct {
	*bank.Bank
	custs []string
}

func newBank(t testing.TB, id, nip, nubanCode string, customers int) *testBank {
	ctx := context.Background()
	b := bank.New(ledger.New(banktest.Schema(t), nil), bank.Profile{ID: id, Name: id, NIPCode: nip, NUBANCode: nubanCode}, banktest.Fees(t))
	if err := b.Setup(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Genesis(ctx, 1000000000000, 200000000000, t0); err != nil {
		t.Fatal(err)
	}
	tb := &testBank{Bank: b}
	for i := 0; i < customers; i++ {
		c, err := b.CreateCustomer(ctx, fmt.Sprintf("c%d", i), fmt.Sprintf("%s customer %d", id, i), "+2348030000001", clock.Date{Y: 1990, M: 1, D: 1}, "22912345678", "", "", t0)
		if err != nil {
			t.Fatal(err)
		}
		acc, err := b.OpenAccount(ctx, fmt.Sprintf("a%d", i), c.ID, bank.ProductCurrent, t0)
		if err != nil {
			t.Fatal(err)
		}
		n := acc.NUBAN
		if _, err := b.CashDeposit(ctx, "open:"+n, n, 50000000, t0); err != nil {
			t.Fatal(err)
		}
		tb.custs = append(tb.custs, n)
	}
	return tb
}

func mustOK(t testing.TB, l *ledger.Ledger) {
	t.Helper()
	c, err := l.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !c.OK() {
		t.Fatal(c)
	}
}

func bal(t testing.TB, b *bank.Bank, code string) int64 {
	t.Helper()
	v, err := b.L.Balance(context.Background(), code)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestNIPTransferEntriesMatchPRD(t *testing.T) {
	ctx := context.Background()
	a := newBank(t, "bank-a", "999001", "901", 1)
	sid := "999001260302090000000000000001"
	before := bal(t, a.Bank, bank.CustomerAccount(a.custs[0]))
	_, c, err := a.SendNIP(ctx, bank.NIPSend{Key: "send-1", From: a.custs[0], Amount: 10000000, SessionID: sid, To: bank.Counterparty{Name: "CHIDI OKAFOR", BankName: "BANK B"}, At: t0.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if c.Fee != 5000 || c.VAT != 375 || c.StampDuty != 5000 {
		t.Fatalf("charges %+v, want ₦50, ₦3.75 and ₦50", c)
	}
	if got := before - bal(t, a.Bank, bank.CustomerAccount(a.custs[0])); got != 10010375 {
		t.Fatalf("customer debited %d, want 10010375", got)
	}
	if bal(t, a.Bank, bank.NIPOutward) != 10000000 || bal(t, a.Bank, bank.FeeIncome) != 5000 || bal(t, a.Bank, bank.VATPayable) != 375 || bal(t, a.Bank, bank.StampPayable) != 5000 {
		t.Fatal("payables do not match the PRD example")
	}
	evs, _ := a.L.Events(ctx, bank.CustomerAccount(a.custs[0]), 1, 10)
	if len(evs) != 4 {
		t.Fatalf("want 4 separate statement lines, got %d", len(evs))
	}
	mustOK(t, a.L)
}

func TestFreeNIPTransferPostsNoFeeLines(t *testing.T) {
	ctx := context.Background()
	a := newBank(t, "bank-a", "999001", "901", 1)
	at := time.Date(2026, 5, 4, 9, 0, 0, 0, clock.Lagos)
	before := bal(t, a.Bank, bank.CustomerAccount(a.custs[0]))
	_, c, err := a.SendNIP(ctx, bank.NIPSend{Key: "send-free", From: a.custs[0], Amount: 300000, SessionID: "999001260504090000000000000001", To: bank.Counterparty{Name: "CHIDI OKAFOR", BankName: "BANK B"}, At: at})
	if err != nil {
		t.Fatal(err)
	}
	if c != (charges.Transfer{}) {
		t.Fatalf("charges %+v, want none", c)
	}
	if got := before - bal(t, a.Bank, bank.CustomerAccount(a.custs[0])); got != 300000 {
		t.Fatalf("customer debited %d, want 300000", got)
	}
	if bal(t, a.Bank, bank.FeeIncome) != 0 || bal(t, a.Bank, bank.VATPayable) != 0 || bal(t, a.Bank, bank.StampPayable) != 0 {
		t.Fatal("a free transfer posted fee, VAT or stamp duty")
	}
	evs, err := a.L.Events(ctx, bank.CustomerAccount(a.custs[0]), 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("want the transfer line only, got %d", len(evs))
	}
	mustOK(t, a.L)
}

func TestStatementLinesCarryNarration(t *testing.T) {
	ctx := context.Background()
	a := newBank(t, "bank-a", "999001", "901", 2)
	sender := bank.Counterparty{Name: "AMA", BankName: "BANK A"}
	to := bank.Counterparty{Name: "CHIDI OKAFOR", BankName: "BANK B"}
	for i, tc := range []struct{ narration, sent, received string }{
		{"", "NIP TRF TO CHIDI OKAFOR/BANK B/000000000001", "NIP TRF FRM AMA/BANK A/000000000001"},
		{"Rent", "NIP TRF TO CHIDI OKAFOR/BANK B/000000000002/Rent", "NIP TRF FRM AMA/BANK A/000000000002/Rent"},
	} {
		sid := fmt.Sprintf("99900126030209000000%010d", i+1)
		if _, _, err := a.SendNIP(ctx, bank.NIPSend{Key: sid, From: a.custs[0], Amount: 100000, SessionID: sid, To: to, At: t0.Add(time.Hour), Narration: tc.narration}); err != nil {
			t.Fatal(err)
		}
		if _, err := a.ReceiveNIP(ctx, sid, a.custs[1], 100000, sender, tc.narration, false, t0.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		sent, _ := a.L.Events(ctx, bank.CustomerAccount(a.custs[0]), 0, 50)
		got, _ := a.L.Events(ctx, bank.CustomerAccount(a.custs[1]), 0, 50)
		if !hasNarration(sent, tc.sent) || !hasNarration(got, tc.received) {
			t.Fatalf("narration %q: want %q and %q on the statements", tc.narration, tc.sent, tc.received)
		}
	}
}

func hasNarration(evs []ledger.Event, want string) bool {
	for _, e := range evs {
		if e.Narration == want {
			return true
		}
	}
	return false
}

func TestOwnAccountTransferHasNoCharges(t *testing.T) {
	ctx := context.Background()
	a := newBank(t, "bank-a", "999001", "901", 2)
	if _, err := a.OwnTransfer(ctx, "own-1", a.custs[0], a.custs[1], 2000000, "", t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if bal(t, a.Bank, bank.FeeIncome)+bal(t, a.Bank, bank.StampPayable) != 0 {
		t.Fatal("own-account transfer at the same bank was charged")
	}
	mustOK(t, a.L)
}

type cbn struct {
	l *ledger.Ledger
}

func newCBN(t testing.TB, banks ...string) *cbn {
	ctx := context.Background()
	l := ledger.New(banktest.Schema(t), nil)
	if _, err := l.OpenAccount(ctx, ledger.Account{Code: "ISSUANCE", Name: "Issuance", Kind: ledger.Asset}); err != nil {
		t.Fatal(err)
	}
	for _, b := range banks {
		if _, err := l.OpenAccount(ctx, ledger.Account{Code: "SETTLE:" + b, Name: b + " settlement", Kind: ledger.Liability}); err != nil {
			t.Fatal(err)
		}
		d := clock.DateOf(t0)
		if _, err := l.Post(ctx, ledger.Entry{Key: "genesis:" + b, Kind: "genesis", PostingDate: d, PostedAt: t0, Narration: "Issuance", Lines: []ledger.Line{{Account: "ISSUANCE", Amount: 1000000000000}, {Account: "SETTLE:" + b, Amount: -1000000000000}}}); err != nil {
			t.Fatal(err)
		}
	}
	return &cbn{l: l}
}

func (c *cbn) settle(t testing.TB, key string, net map[string]int64, at time.Time) {
	lines := []ledger.Line{}
	for b, n := range net {
		if n != 0 {
			lines = append(lines, ledger.Line{Account: "SETTLE:" + b, Amount: -n})
		}
	}
	if len(lines) == 0 {
		return
	}
	d := clock.DateOf(at)
	if _, err := c.l.Post(context.Background(), ledger.Entry{Key: key, Kind: "rtgs", PostingDate: d, PostedAt: at, Narration: key, Lines: lines}); err != nil {
		t.Fatal(err)
	}
}

func TestMoneyIsConservedAcrossBanksUnderFaults(t *testing.T) {
	ctx := context.Background()
	banks := map[string]*testBank{
		"bank-a": newBank(t, "bank-a", "999001", "901", 4),
		"bank-b": newBank(t, "bank-b", "999002", "902", 4),
	}
	central := newCBN(t, "bank-a", "bank-b")
	names := []string{"bank-a", "bank-b"}
	customerTotal := func() int64 {
		var sum int64
		for _, b := range banks {
			for _, n := range b.custs {
				sum += bal(t, b.Bank, bank.CustomerAccount(n))
			}
			sum += bal(t, b.Bank, bank.FeeIncome) + bal(t, b.Bank, bank.VATPayable) + bal(t, b.Bank, bank.StampPayable)
		}
		return sum
	}
	start := customerTotal()
	credited := map[string]int{}
	pending := map[string]map[string]int64{}
	clockAt := t0.Add(time.Hour)
	seq := 0
	session := 0
	rapid.Check(t, func(rt *rapid.T) {
		session++
		outward := map[string]int64{}
		inward := map[string]int64{}
		n := rapid.IntRange(1, 6).Draw(rt, "transfers")
		for i := 0; i < n; i++ {
			seq++
			clockAt = clockAt.Add(time.Minute)
			fromName := rapid.SampledFrom(names).Draw(rt, "from")
			toName := names[0]
			if fromName == names[0] {
				toName = names[1]
			}
			from, to := banks[fromName], banks[toName]
			src := rapid.SampledFrom(from.custs).Draw(rt, "src")
			dst := rapid.SampledFrom(to.custs).Draw(rt, "dst")
			amount := int64(rapid.IntRange(100, 2000000).Draw(rt, "naira")) * 100
			outcome := rapid.SampledFrom([]string{"success", "fail", "timeout_success", "timeout_fail"}).Draw(rt, "outcome")
			dupes := rapid.IntRange(0, 2).Draw(rt, "duplicates")
			sid := fmt.Sprintf("%s%s%012d", from.P.NIPCode, clockAt.Format("060102150405"), seq)
			p, _, err := from.SendNIP(ctx, bank.NIPSend{Key: "send:" + sid, From: src, Amount: amount, SessionID: sid, To: bank.Counterparty{Name: "X", BankName: to.P.Name}, At: clockAt})
			if errors.Is(err, ledger.ErrInsufficientFunds) || errors.Is(err, ledger.ErrDailyLimit) {
				continue
			}
			if err != nil {
				rt.Fatal(err)
			}
			if _, _, err := from.SendNIP(ctx, bank.NIPSend{Key: "send:" + sid, From: src, Amount: amount, SessionID: sid, To: bank.Counterparty{Name: "X", BankName: to.P.Name}, At: clockAt}); err != nil {
				rt.Fatalf("retried send: %v", err)
			}
			receiverCredits := outcome == "success" || outcome == "timeout_success"
			if receiverCredits {
				for k := 0; k <= dupes; k++ {
					r, err := to.ReceiveNIP(ctx, sid, dst, amount, bank.Counterparty{Name: "Y", BankName: from.P.Name}, "", false, clockAt)
					if err != nil {
						rt.Fatal(err)
					}
					if !r.Replayed {
						credited[sid]++
					}
				}
				outward[fromName] += amount
				inward[toName] += amount
			} else {
				if _, err := from.ReverseNIP(ctx, p.EntryID, sid, clockAt.Add(time.Minute)); err != nil {
					rt.Fatal(err)
				}
				if _, err := from.ReverseNIP(ctx, p.EntryID, sid, clockAt.Add(2*time.Minute)); err != nil && !errors.Is(err, ledger.ErrAlreadyReversed) {
					rt.Fatal(err)
				}
			}
		}
		key := fmt.Sprintf("S%05d", session)
		net := map[string]int64{}
		for _, name := range names {
			if _, err := banks[name].Settle(ctx, key, outward[name], inward[name], clockAt); err != nil {
				rt.Fatal(err)
			}
			net[name] = inward[name] - outward[name]
		}
		central.settle(t, "rtgs:"+key, net, clockAt)
		_ = pending
		for sid, c := range credited {
			if c != 1 {
				rt.Fatalf("session %s credited %d times", sid, c)
			}
		}
		if got := customerTotal(); got != start {
			rt.Fatalf("customer money plus charges moved from %d to %d", start, got)
		}
		for _, name := range names {
			b := banks[name]
			mustOK(t, b.L)
			if bal(t, b.Bank, bank.NIPOutward) != 0 || bal(t, b.Bank, bank.NIPInward) != 0 {
				rt.Fatalf("%s NIP accounts not cleared after settlement", name)
			}
			atCBN, err := central.l.Balance(ctx, "SETTLE:"+name)
			if err != nil {
				rt.Fatal(err)
			}
			if atCBN != bal(t, b.Bank, bank.Settlement) {
				rt.Fatalf("%s settlement account %d does not match the CBN's %d", name, bal(t, b.Bank, bank.Settlement), atCBN)
			}
		}
		mustOK(t, central.l)
		var total int64
		for _, name := range names {
			v, _ := central.l.Balance(ctx, "SETTLE:"+name)
			total += v
		}
		if total != 2000000000000 {
			rt.Fatalf("money at the CBN changed: %d", total)
		}
	})
}

func TestConcurrentPostingKeepsInvariants(t *testing.T) {
	ctx := context.Background()
	a := newBank(t, "bank-a", "999001", "901", 6)
	start := int64(0)
	for _, n := range a.custs {
		start += bal(t, a.Bank, bank.CustomerAccount(n))
	}
	var wg sync.WaitGroup
	errs := make(chan error, 1000)
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				from := a.custs[(w+i)%len(a.custs)]
				to := a.custs[(w*7+i*3+1)%len(a.custs)]
				if from == to {
					continue
				}
				key := fmt.Sprintf("c:%d:%d", w, i)
				if _, err := a.OwnTransfer(ctx, key, from, to, int64(100+i)*100, "", t0.Add(time.Duration(i)*time.Second)); err != nil && !errors.Is(err, ledger.ErrInsufficientFunds) && !errors.Is(err, ledger.ErrDailyLimit) {
					errs <- err
				}
				if _, err := a.OwnTransfer(ctx, key, from, to, int64(100+i)*100, "", t0.Add(time.Duration(i)*time.Second)); err != nil && !errors.Is(err, ledger.ErrInsufficientFunds) && !errors.Is(err, ledger.ErrDailyLimit) {
					errs <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	end := int64(0)
	for _, n := range a.custs {
		v := bal(t, a.Bank, bank.CustomerAccount(n))
		if v < 0 {
			t.Fatalf("negative balance %d", v)
		}
		end += v
	}
	if end != start {
		t.Fatalf("own transfers changed total customer money: %d to %d", start, end)
	}
	mustOK(t, a.L)
}

func TestReviewRegressions(t *testing.T) {
	ctx := context.Background()
	a := newBank(t, "bank-a", "999001", "901", 3)
	c0, c1 := a.custs[0], a.custs[1]
	at := t0.Add(2 * time.Hour)
	p1, err := a.CashDeposit(ctx, "retry-1", c0, 100000, at)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := a.CashDeposit(ctx, "retry-1", c0, 100000, at.Add(time.Second))
	if err != nil || !p2.Replayed || p2.EntryID != p1.EntryID {
		t.Fatalf("a retry one second later was not a replay: %+v %v", p2, err)
	}
	sid := "999001260302110000000000000777"
	sent, _, err := a.SendNIP(ctx, bank.NIPSend{Key: "nip-rev:" + sid, From: c0, Amount: 2000000, SessionID: sid, To: bank.Counterparty{Name: "X", BankName: "BANK B"}, At: at})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.L.SetStatus(ctx, bank.CustomerAccount(c0), "frozen"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ReverseNIP(ctx, sent.EntryID, sid, at.Add(time.Hour)); err != nil {
		t.Fatalf("refund to a frozen account, or a client key colliding with the reversal key: %v", err)
	}
	if _, err := a.ReceiveNIP(ctx, "123", c1, 100, bank.Counterparty{Name: "X", BankName: "Y"}, "", false, at); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatalf("short session ID: %v", err)
	}
	if _, err := a.L.OpenAccount(ctx, ledger.Account{Code: bank.CustomerAccount("9020000027"), Name: "Tier one", Kind: ledger.Liability, Customer: true, NUBAN: "9020000027", Product: "savings", Owner: "tier1", KYCTier: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CashDeposit(ctx, "t1dep", "9020000027", 20000000, at); err != nil {
		t.Fatal(err)
	}
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
	if err := a.FreezeAccount(ctx, "9020000027"); err != nil {
		t.Fatal(err)
	}
	if status, err := a.AccountStatus(ctx, "9020000027"); err != nil || status != "frozen" {
		t.Fatalf("account status %q %v", status, err)
	}
	mustOK(t, a.L)
}
