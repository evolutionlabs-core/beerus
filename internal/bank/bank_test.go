package bank_test

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"pgregory.net/rapid"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/banktest"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/charges"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/nip"
)

func TestMain(m *testing.M) {
	flag.Parse()
	given := false
	flag.Visit(func(f *flag.Flag) { given = given || f.Name == "rapid.checks" })
	if !given {
		if err := flag.Set("rapid.checks", "20"); err != nil {
			panic(err)
		}
	}
	os.Exit(m.Run())
}

var t0 = time.Date(2026, 3, 2, 9, 0, 0, 0, clock.Lagos)

type testBank struct {
	*bank.Bank
	custs []string
	free  *[]string
}

func (b *testBank) take(n int) *testBank {
	rest := *b.free
	*b.free = rest[n:]
	return &testBank{Bank: b.Bank, custs: rest[:n]}
}

func newBank(t testing.TB, id, nip, nubanCode string, customers int) *testBank {
	ctx := context.Background()
	b := bank.New(ledger.New(banktest.Schema(t), nil), bank.Profile{ID: id, Name: id, NIPCode: nip, NUBANCode: nubanCode, PasswordCost: bcrypt.MinCost}, banktest.Fees(t))
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
	free := slices.Clone(tb.custs)
	tb.free = &free
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

func transferPostings(t *testing.T, a *testBank) {
	ctx := context.Background()
	to := bank.Counterparty{Name: "CHIDI OKAFOR", BankName: "BANK B"}
	rows := []struct {
		name      string
		amount    int64
		noCharges bool
		own       bool
		fee, vat  int64
		stamp     int64
		lines     int
		err       error
		msg       string
	}{
		{name: "paid NIP", amount: 10000000, fee: 5000, vat: 375, stamp: 5000, lines: 4},
		{name: "free NIP", amount: 300000, lines: 1},
		{name: "closure send", amount: 2000000, noCharges: true, lines: 1},
		{name: "own account", amount: 2000000, own: true, lines: 1},
		{name: "zero", amount: 0, err: ledger.ErrInvalid, msg: "ledger: invalid entry: amount must be positive"},
		{name: "the largest amount", amount: math.MaxInt64, err: ledger.ErrOverflow, msg: "ledger: amount overflow"},
		{name: "more than the balance", amount: 1 << 50, err: ledger.ErrInsufficientFunds, msg: "ledger: insufficient funds"},
	}
	for i, r := range rows {
		from := a.custs[i%len(a.custs)]
		accounts := []string{bank.NIPOutward, bank.FeeIncome, bank.VATPayable, bank.StampPayable}
		before := make([]int64, len(accounts))
		for j, code := range accounts {
			before[j] = bal(t, a.Bank, code)
		}
		own := bal(t, a.Bank, bank.CustomerAccount(from))
		evs, _ := a.L.Events(ctx, bank.CustomerAccount(from), 0, 1000)
		lines := len(evs)
		sid := fmt.Sprintf("99900126030208%08d%08d", 1, i+1)
		var err error
		var c charges.Transfer
		if r.own {
			_, err = a.OwnTransfer(ctx, sid, from, a.custs[(i+1)%len(a.custs)], r.amount, "", t0.Add(time.Hour))
		} else {
			_, c, err = a.SendNIP(ctx, bank.NIPSend{Key: sid, From: from, Amount: r.amount, SessionID: sid, To: to, At: t0.Add(time.Hour), NoCharges: r.noCharges})
		}
		if r.err != nil {
			wantErr(t, err, r.err, r.msg)
			continue
		}
		if err != nil || c != (charges.Transfer{Fee: r.fee, VAT: r.vat, StampDuty: r.stamp}) {
			t.Fatalf("%s: charges %+v, %v", r.name, c, err)
		}
		if got := own - bal(t, a.Bank, bank.CustomerAccount(from)); got != r.amount+r.fee+r.vat+r.stamp {
			t.Errorf("%s: customer debited %d", r.name, got)
		}
		want := []int64{r.amount, r.fee, r.vat, r.stamp}
		if r.own {
			want[0] = 0
		}
		for j, code := range accounts {
			if got := bal(t, a.Bank, code) - before[j]; got != want[j] {
				t.Errorf("%s: %s moved %d, want %d", r.name, code, got, want[j])
			}
		}
		if evs, _ := a.L.Events(ctx, bank.CustomerAccount(from), 0, 1000); len(evs)-lines != r.lines {
			t.Errorf("%s: %d statement lines, want %d", r.name, len(evs)-lines, r.lines)
		}
	}
	mustOK(t, a.L)
}

func narration(t *testing.T, a *testBank) {
	ctx := context.Background()
	sender := bank.Counterparty{Name: "AMA", BankName: "BANK A"}
	to := bank.Counterparty{Name: "CHIDI OKAFOR", BankName: "BANK B"}
	for i, tc := range []struct{ narration, sent, received string }{
		{"", "NIP TRF TO CHIDI OKAFOR/BANK B/000000000001", "NIP TRF FRM AMA/BANK A/000000000001"},
		{"Rent", "NIP TRF TO CHIDI OKAFOR/BANK B/000000000002/Rent", "NIP TRF FRM AMA/BANK A/000000000002/Rent"},
	} {
		sid := fmt.Sprintf("99900126030212000000%010d", i+1)
		if _, _, err := a.SendNIP(ctx, bank.NIPSend{Key: sid, From: a.custs[0], Amount: 100000, SessionID: sid, To: to, At: t0.Add(time.Hour), Narration: tc.narration}); err != nil {
			t.Fatal(err)
		}
		if _, err := credit(ctx, a.Bank, sid, a.custs[1], 100000, sender, tc.narration, t0.Add(time.Hour)); err != nil {
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

func TestPostings(t *testing.T) {
	a := newBank(t, "bank-a", "999001", "901", 18)
	for _, s := range []struct {
		name  string
		custs int
		run   func(*testing.T, *testBank)
	}{
		{"open sends", 1, openSends},
		{"transfers", 5, transferPostings},
		{"narration", 2, narration},
		{"reversal", 1, reverseNIP},
		{"limits and closures", 3, limitsAndClosures},
		{"concurrent posting", 6, concurrentPostings},
	} {
		t.Run(s.name, func(t *testing.T) { s.run(t, a.take(s.custs)) })
	}
	mustOK(t, a.L)
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
					r, err := credit(ctx, to.Bank, sid, dst, amount, bank.Counterparty{Name: "Y", BankName: from.P.Name}, "", clockAt)
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

func concurrentPostings(t *testing.T, a *testBank) {
	ctx := context.Background()
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
			for i := 0; i < 15; i++ {
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

func credit(ctx context.Context, b *bank.Bank, sessionID, to string, amount int64, from bank.Counterparty, narration string, at time.Time) (bank.CreditAnswer, error) {
	ans, err := b.Credit(ctx, bank.NIPCredit{SessionID: sessionID, FromCode: from.BankCode, FromBankName: from.BankName, FromNUBAN: from.AccountNumber, FromName: from.Name, ToNUBAN: to, Amount: amount, Narration: narration, CreatedAt: at}, at)
	if err == nil && ans.Code != nip.OK {
		err = fmt.Errorf("credit session %s refused with code %s", sessionID, ans.Code)
	}
	return ans, err
}
