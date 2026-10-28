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
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

var t0 = time.Date(2026, 3, 2, 9, 0, 0, 0, clock.Lagos)

type testBank struct {
	*bank.Bank
	custs []string
}

func newBank(t testing.TB, id, nip, nubanCode string, customers int) *testBank {
	ctx := context.Background()
	b := bank.New(ledger.New(banktest.Schema(t), nil), bank.Profile{ID: id, Name: id, NIPCode: nip, NUBANCode: nubanCode})
	if err := b.Setup(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Genesis(ctx, 1000000000000, 200000000000, t0); err != nil {
		t.Fatal(err)
	}
	tb := &testBank{Bank: b}
	for i := 0; i < customers; i++ {
		n := world.NUBAN(nubanCode, uint64(1000+i))
		if _, err := b.OpenCustomer(ctx, n, fmt.Sprintf("%s customer %d", id, i), "current", "owner-"+id, 3); err != nil {
			t.Fatal(err)
		}
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
	before := bal(t, a.Bank, bank.Customer(a.custs[0]))
	_, c, err := a.SendNIP(ctx, bank.NIPSend{Key: "send-1", From: a.custs[0], Amount: 10000000, SessionID: sid, Beneficiary: "CHIDI OKAFOR", BankName: "BANK B", At: t0.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if c.Fee != 5000 || c.VAT != 375 || c.StampDuty != 5000 {
		t.Fatalf("charges %+v, want ₦50, ₦3.75 and ₦50", c)
	}
	if got := before - bal(t, a.Bank, bank.Customer(a.custs[0])); got != 10010375 {
		t.Fatalf("customer debited %d, want 10010375", got)
	}
	if bal(t, a.Bank, bank.NIPOutward) != 10000000 || bal(t, a.Bank, bank.FeeIncome) != 5000 || bal(t, a.Bank, bank.VATPayable) != 375 || bal(t, a.Bank, bank.StampPayable) != 5000 {
		t.Fatal("payables do not match the PRD example")
	}
	evs, _ := a.L.Events(ctx, bank.Customer(a.custs[0]), 1, 10)
	if len(evs) != 4 {
		t.Fatalf("want 4 separate statement lines, got %d", len(evs))
	}
	mustOK(t, a.L)
}

func TestOwnAccountTransferHasNoCharges(t *testing.T) {
	ctx := context.Background()
	a := newBank(t, "bank-a", "999001", "901", 2)
	if _, err := a.OwnTransfer(ctx, "own-1", a.custs[0], a.custs[1], 2000000, t0.Add(time.Hour)); err != nil {
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
				sum += bal(t, b.Bank, bank.Customer(n))
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
			p, _, err := from.SendNIP(ctx, bank.NIPSend{Key: "send:" + sid, From: src, Amount: amount, SessionID: sid, Beneficiary: "X", BankName: to.P.Name, At: clockAt})
			if errors.Is(err, ledger.ErrInsufficientFunds) || errors.Is(err, ledger.ErrDailyLimit) {
				continue
			}
			if err != nil {
				rt.Fatal(err)
			}
			if _, _, err := from.SendNIP(ctx, bank.NIPSend{Key: "send:" + sid, From: src, Amount: amount, SessionID: sid, Beneficiary: "X", BankName: to.P.Name, At: clockAt}); err != nil {
				rt.Fatalf("retried send: %v", err)
			}
			receiverCredits := outcome == "success" || outcome == "timeout_success"
			if receiverCredits {
				for k := 0; k <= dupes; k++ {
					r, err := to.ReceiveNIP(ctx, sid, dst, amount, "Y", from.P.Name, clockAt)
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
		start += bal(t, a.Bank, bank.Customer(n))
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
				if _, err := a.OwnTransfer(ctx, key, from, to, int64(100+i)*100, t0.Add(time.Duration(i)*time.Second)); err != nil && !errors.Is(err, ledger.ErrInsufficientFunds) && !errors.Is(err, ledger.ErrDailyLimit) {
					errs <- err
				}
				if _, err := a.OwnTransfer(ctx, key, from, to, int64(100+i)*100, t0.Add(time.Duration(i)*time.Second)); err != nil && !errors.Is(err, ledger.ErrInsufficientFunds) && !errors.Is(err, ledger.ErrDailyLimit) {
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
		v := bal(t, a.Bank, bank.Customer(n))
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

func TestMonthEndChargesOnce(t *testing.T) {
	ctx := context.Background()
	a := newBank(t, "bank-a", "999001", "901", 2)
	march := time.Date(2026, 3, 10, 12, 0, 0, 0, clock.Lagos)
	for i := 0; i < 3; i++ {
		if _, err := a.OwnTransfer(ctx, fmt.Sprintf("m%d", i), a.custs[0], a.custs[1], 1000000, march.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	end := time.Date(2026, 3, 31, 23, 30, 0, 0, clock.Lagos)
	before := bal(t, a.Bank, bank.Customer(a.custs[0]))
	r, err := a.MonthEnd(ctx, time.March, 2026, end)
	if err != nil {
		t.Fatal(err)
	}
	if r.Accounts != 2 {
		t.Fatalf("charged %d accounts", r.Accounts)
	}
	want := int64(4*400) + 120 + 3000 + 225
	if got := before - bal(t, a.Bank, bank.Customer(a.custs[0])); got != want {
		t.Fatalf("sender charged %d, want %d", got, want)
	}
	again, err := a.MonthEnd(ctx, time.March, 2026, end)
	if err != nil || again.Accounts != 0 {
		t.Fatalf("month end ran twice: %+v %v", again, err)
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
	sent, _, err := a.SendNIP(ctx, bank.NIPSend{Key: "nip-rev:" + sid, From: c0, Amount: 2000000, SessionID: sid, Beneficiary: "X", BankName: "BANK B", At: at})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.L.SetStatus(ctx, bank.Customer(c0), "frozen"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ReverseNIP(ctx, sent.EntryID, sid, at.Add(time.Hour)); err != nil {
		t.Fatalf("refund to a frozen account, or a client key colliding with the reversal key: %v", err)
	}
	if _, err := a.ReceiveNIP(ctx, "123", c1, 100, "X", "Y", at); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatalf("short session ID: %v", err)
	}
	if _, err := a.MonthEnd(ctx, time.March, 2026, time.Date(2026, 3, 31, 23, 0, 0, 0, clock.Lagos)); err != nil {
		t.Fatalf("month end with a frozen customer: %v", err)
	}
	if v := bal(t, a.Bank, bank.Customer(c1)); v >= 50000000 {
		t.Fatal("month end skipped the other customers")
	}
	if _, err := a.OpenCustomer(ctx, "9020000019", "Stranger", "current", "someone-else", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := a.OwnTransfer(ctx, "own-x", c1, "9020000019", 1000, at); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatalf("own transfer between different owners: %v", err)
	}
	if _, err := a.OpenCustomer(ctx, "9020000027", "Tier one", "savings", "tier1", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CashDeposit(ctx, "t1dep", "9020000027", 20000000, at); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CashWithdrawal(ctx, "t1wd", "9020000027", 10000000, at); !errors.Is(err, ledger.ErrDailyLimit) {
		t.Fatalf("tier 1 cash withdrawal over the daily limit: %v", err)
	}
	mustOK(t, a.L)
}
