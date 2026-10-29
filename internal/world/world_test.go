package world

import (
	"errors"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

func testWorld() (*World, time.Time) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, clock.Lagos)
	return New(Config{Seed: 42, Genesis: clock.DateOf(now).AddDays(-760)}, Overrides{}), now
}

func TestNUBANValid(t *testing.T) {
	w, _ := testWorld()
	for _, p := range w.Personas() {
		for _, k := range p.Accounts {
			a, _ := w.Account(k)
			inst, _ := w.Institution(a.Institution)
			if !ValidNUBAN(inst.NUBANCode, a.NUBAN) {
				t.Fatalf("invalid NUBAN %s", a.NUBAN)
			}
		}
	}
}

func TestChargesMatchRules(t *testing.T) {
	if VAT(1000) != 75 || VAT(2500) != 188 || VAT(5000) != 375 {
		t.Fatalf("vat %d %d %d", VAT(1000), VAT(2500), VAT(5000))
	}
	if NIPFee(500000) != 1000 || NIPFee(500100) != 2500 || NIPFee(5000100) != 5000 {
		t.Fatal("fee bands")
	}
	d25 := clock.Date{Y: 2025, M: 12, D: 31}
	d26 := clock.Date{Y: 2026, M: 1, D: 1}
	if StampDuty(1000000, d25, false) != 0 || StampDuty(1000000, d26, false) != 5000 || StampDuty(999900, d26, false) != 0 || StampDuty(1000000, d26, true) != 0 {
		t.Fatal("stamp duty")
	}
	if EMTL(1000000, d25) != 5000 || EMTL(1000000, d26) != 0 {
		t.Fatal("emtl")
	}
}

func TestBalancesChainAndNeverNegative(t *testing.T) {
	w, now := testWorld()
	for _, p := range w.Personas() {
		for _, k := range p.Accounts {
			a, _ := w.Account(k)
			ls := w.LinesFor(k, now)
			if len(ls) < 20 {
				t.Fatalf("%s: only %d lines", k, len(ls))
			}
			bal := a.Opening
			for _, l := range ls {
				bal += l.Amount
				if l.BalanceAfter != bal {
					t.Fatalf("%s: balance chain broken at %s", k, l.Key)
				}
				if bal < 0 {
					t.Fatalf("%s: negative balance at %s", k, l.Key)
				}
			}
			got, _ := w.LedgerAt(k, now)
			if got != bal {
				t.Fatalf("%s: ledger %d != %d", k, got, bal)
			}
		}
	}
}

func TestDeterministic(t *testing.T) {
	a, now := testWorld()
	b, _ := testWorld()
	for _, p := range a.Personas() {
		for _, k := range p.Accounts {
			la, lb := a.LinesFor(k, now), b.LinesFor(k, now)
			if len(la) != len(lb) {
				t.Fatalf("%s: %d vs %d", k, len(la), len(lb))
			}
			for i := range la {
				if la[i].Key != lb[i].Key || la[i].Amount != lb[i].Amount || la[i].SessionID != lb[i].SessionID {
					t.Fatalf("%s: line %d differs", k, i)
				}
			}
		}
	}
}

func TestFetchStableAsClockMoves(t *testing.T) {
	w, now := testWorld()
	k := w.Personas()[0].Accounts[0]
	from := clock.DateOf(now).AddDays(-90)
	before, _ := w.Fetch(k, now, from)
	after, _ := w.Fetch(k, now.Add(5*24*time.Hour), from)
	idx := map[string]Presented{}
	for _, p := range after {
		if !p.Pending {
			idx[p.Key] = p
		}
	}
	seen := map[int64]bool{}
	for _, p := range before {
		if seen[p.Seq] {
			t.Fatalf("duplicate seq %d", p.Seq)
		}
		seen[p.Seq] = true
		if p.Pending {
			continue
		}
		q, ok := idx[p.Key]
		if !ok || q.Amount != p.Amount || q.Seq != p.Seq || q.BalanceAfter != p.BalanceAfter {
			t.Fatalf("posted line %s changed after the clock moved", p.Key)
		}
	}
}

func TestPendingPostsLater(t *testing.T) {
	w, now := testWorld()
	found := false
	for _, p := range w.Personas() {
		for _, k := range p.Accounts {
			for _, l := range w.LinesFor(k, now) {
				if l.HasPending() {
					found = true
					if !l.PendingAt.Before(l.BookedAt) || l.PendingSeq >= l.PostedSeq {
						t.Fatalf("pending line %s posts before it appears", l.Key)
					}
				}
			}
		}
	}
	if !found {
		t.Fatal("no pending lines generated")
	}
}

func TestScenariosInject(t *testing.T) {
	w, now := testWorld()
	k := w.Personas()[0].Accounts[0]
	for _, s := range []string{"debit_then_reversal", "fee_vat_levy_lines", "identical_same_day_fees", "pending_amount_change", "own_account_transfer_across_banks", "renarration", "unstable_ids", "bank_outage"} {
		if _, err := w.RunScenario(s, now.Add(time.Minute), ScenarioParams{Account: k}); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if !w.Down("bank-a", now.Add(time.Hour)) {
		t.Fatal("outage not applied")
	}
	re := New(w.Config(), w.Overrides())
	la, lb := w.LinesFor(k, now.Add(48*time.Hour)), re.LinesFor(k, now.Add(48*time.Hour))
	if len(la) != len(lb) {
		t.Fatalf("restart changed lines: %d vs %d", len(la), len(lb))
	}
	for i := range la {
		if la[i].Key != lb[i].Key || la[i].BalanceAfter != lb[i].BalanceAfter {
			t.Fatalf("restart changed line %d", i)
		}
	}
}

func TestScenarioLinesHaveValueDates(t *testing.T) {
	w, now := testWorld()
	k := w.Personas()[0].Accounts[0]
	for _, s := range []string{"debit_then_reversal", "late_reversal", "fee_vat_levy_lines", "identical_same_day_fees", "pending_amount_change", "own_account_transfer_across_banks"} {
		if _, err := w.RunScenario(s, now.Add(time.Minute), ScenarioParams{Account: k}); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	for _, p := range w.Personas() {
		for _, acct := range p.Accounts {
			ls, _ := w.Fetch(acct, now.Add(72*time.Hour), clock.DateOf(now).AddDays(-1))
			for _, l := range ls {
				if l.ValueDate.IsZero() || l.ValueDate.Y < 2000 {
					t.Fatalf("line %s has value date %v", l.Key, l.ValueDate)
				}
			}
		}
	}
}

func TestTransfers(t *testing.T) {
	w, now := testWorld()
	ad := w.Personas()[0]
	var aCur, bCur, aSav Account
	for _, k := range ad.Accounts {
		a, _ := w.Account(k)
		switch {
		case a.Institution == "bank-a" && a.Type == "current":
			aCur = a
		case a.Institution == "bank-b":
			bCur = a
		case a.Type == "savings":
			aSav = a
		}
	}
	before, _ := w.LedgerAt(bCur.Key, now)
	r, err := w.Transfer(now, TransferRequest{From: aCur.NUBAN, To: bCur.NUBAN, Amount: 2500000})
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != "nip" || len(r.SessionID) != 30 || r.Fee != 2500 || r.VAT != 188 || r.StampDuty != 5000 || !r.Credited {
		t.Fatalf("cross-bank transfer: %+v", r)
	}
	after, _ := w.LedgerAt(bCur.Key, now)
	if after-before != 2500000 {
		t.Fatalf("recipient credited %d", after-before)
	}
	r2, err := w.Transfer(now, TransferRequest{From: aCur.Key, To: aSav.Key, Amount: 2000000})
	if err != nil || r2.Kind != "intra_bank" || r2.Fee != 0 || r2.StampDuty != 0 {
		t.Fatalf("own intra-bank transfer: %+v %v", r2, err)
	}
	r3, err := w.Transfer(now, TransferRequest{From: aCur.Key, To: "0123456789", ToBank: "Bank C", ToName: "Emeka Nwosu", Amount: 500000})
	if err != nil || r3.Credited || r3.Fee != 1000 || r3.StampDuty != 0 {
		t.Fatalf("outside transfer: %+v %v", r3, err)
	}
	if _, err := w.Transfer(now, TransferRequest{From: aCur.Key, To: bCur.Key, Amount: 1 << 50}); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("overdraw: %v", err)
	}
	ls, _ := w.Fetch(aCur.Key, now, clock.DateOf(now))
	bal := int64(0)
	for _, l := range ls {
		if l.SessionID == r.SessionID {
			bal += l.Amount
		}
	}
	if bal != -2507688 {
		t.Fatalf("sender lines for the session sum to %d", bal)
	}
}
