package world

import (
	"errors"
	"testing"
	"time"
)

func accountsAt(w *World, inst string) []Account {
	out := []Account{}
	for _, a := range w.Accounts() {
		if a.Institution == inst {
			out = append(out, a)
		}
	}
	return out
}

func linesBetween(w *World, account string, from, to time.Time) []Line {
	out := []Line{}
	for _, l := range w.LinesFor(account, to) {
		if !l.firstAppear().Before(from) {
			out = append(out, l)
		}
	}
	return out
}

func TestSuspensionFreezesTheBank(t *testing.T) {
	plain, now := testWorld()
	w, _ := testWorld()
	if !w.Suspend("bank-b", now) || w.Suspend("bank-b", now) {
		t.Fatal("suspend should succeed once")
	}
	later := now.Add(10 * 24 * time.Hour)
	for _, a := range accountsAt(w, "bank-b") {
		if got := linesBetween(w, a.Key, now, later); len(got) != 0 {
			t.Fatalf("%s moved while suspended: %+v", a.Key, got[0])
		}
		before, want := w.LinesFor(a.Key, now.Add(-time.Second)), plain.LinesFor(a.Key, now.Add(-time.Second))
		if len(before) != len(want) {
			t.Fatalf("%s history changed: %d lines, want %d", a.Key, len(before), len(want))
		}
	}
	moved := 0
	for _, a := range accountsAt(w, "bank-a") {
		for _, l := range linesBetween(w, a.Key, now, later) {
			if l.Counterparty != nil && l.Counterparty.BankName == "Bank B" && l.Channel == "nip" {
				t.Fatalf("bank-a sent money to suspended bank-b: %+v", l)
			}
			moved++
		}
	}
	if moved == 0 {
		t.Fatal("bank-a stopped moving too")
	}
	b := accountsAt(w, "bank-b")[0]
	a := accountsAt(w, "bank-a")[0]
	if _, err := w.Transfer(now.Add(time.Hour), TransferRequest{From: b.Key, To: a.Key, Amount: 100000}); !errors.Is(err, ErrBankSuspended) {
		t.Fatalf("transfer out of a suspended bank: %v", err)
	}
	bal, _ := w.LedgerAt(a.Key, now.Add(time.Hour))
	if _, err := w.Transfer(now.Add(time.Hour), TransferRequest{From: a.Key, To: b.Key, Amount: 100000}); !errors.Is(err, ErrBankSuspended) {
		t.Fatalf("transfer into a suspended bank: %v", err)
	}
	if after, _ := w.LedgerAt(a.Key, now.Add(time.Hour)); after != bal {
		t.Fatalf("sender was debited: %d then %d", bal, after)
	}
	if _, err := w.RunScenario("debit_then_reversal", now.Add(time.Hour), ScenarioParams{Account: b.Key}); !errors.Is(err, ErrBankSuspended) {
		t.Fatalf("scenario on a suspended bank: %v", err)
	}
	if !w.Resume("bank-b", later) {
		t.Fatal("resume")
	}
	resumed := 0
	for _, acc := range accountsAt(w, "bank-b") {
		resumed += len(linesBetween(w, acc.Key, later, later.Add(20*24*time.Hour)))
		if got := linesBetween(w, acc.Key, now, later); len(got) != 0 {
			t.Fatalf("%s gained lines inside the suspension after resume", acc.Key)
		}
	}
	if resumed == 0 {
		t.Fatal("bank-b did not move after resume")
	}
	again := New(w.Config(), w.Overrides())
	for _, acc := range w.Accounts() {
		x, y := w.LinesFor(acc.Key, later.Add(20*24*time.Hour)), again.LinesFor(acc.Key, later.Add(20*24*time.Hour))
		if len(x) != len(y) {
			t.Fatalf("%s is not rebuilt the same from overrides: %d vs %d", acc.Key, len(x), len(y))
		}
	}
}

func TestOutageDelaysActivity(t *testing.T) {
	plain, now := testWorld()
	w, _ := testWorld()
	start := now.Add(24 * time.Hour).Truncate(24 * time.Hour).Add(6 * time.Hour)
	end := start.Add(14 * time.Hour)
	w.AddOutage(Outage{Institution: "bank-a", From: start, To: end})
	horizon := end.Add(5 * 24 * time.Hour)
	delayed := 0
	for _, a := range accountsAt(w, "bank-a") {
		for _, l := range linesBetween(w, a.Key, start, end) {
			if l.firstAppear().Before(end) {
				t.Fatalf("%s line appears during the outage: %+v", a.Key, l)
			}
		}
		got, want := linesBetween(w, a.Key, start, horizon), linesBetween(plain, a.Key, start, horizon)
		for _, l := range want {
			if l.firstAppear().Before(end) {
				delayed++
			}
		}
		var sum, wantSum int64
		for _, l := range got {
			sum += l.Amount
		}
		for _, l := range want {
			wantSum += l.Amount
		}
		if len(got) != len(want) || sum != wantSum {
			t.Fatalf("%s lost activity in the outage: %d lines %d, want %d lines %d", a.Key, len(got), sum, len(want), wantSum)
		}
	}
	if delayed == 0 {
		t.Fatal("no activity fell in the outage window; pick another window")
	}
	a := accountsAt(w, "bank-a")[0]
	b := accountsAt(w, "bank-b")[0]
	if _, err := w.Transfer(start.Add(time.Hour), TransferRequest{From: b.Key, To: a.Key, Amount: 100000}); !errors.Is(err, ErrBankUnavailable) {
		t.Fatalf("transfer into a bank that is down: %v", err)
	}
	if _, err := w.Transfer(end.Add(time.Minute), TransferRequest{From: b.Key, To: a.Key, Amount: 100000}); err != nil {
		t.Fatalf("transfer after the outage: %v", err)
	}
}

func TestOpenAccountKeepsHistory(t *testing.T) {
	plain, now := testWorld()
	w, _ := testWorld()
	acc, err := w.OpenAccount("tunde", "bank-b", "current", now)
	if err != nil {
		t.Fatal(err)
	}
	if acc.Institution != "bank-b" || acc.Owner != "tunde" || !ValidNUBAN("902", acc.NUBAN) {
		t.Fatalf("account: %+v", acc)
	}
	for _, a := range plain.Accounts() {
		x, y := plain.LinesFor(a.Key, now), w.LinesFor(a.Key, now)
		if len(x) != len(y) {
			t.Fatalf("%s history changed: %d vs %d lines", a.Key, len(x), len(y))
		}
		for i := range x {
			if x[i].Key != y[i].Key || x[i].Amount != y[i].Amount {
				t.Fatalf("%s line %d changed", a.Key, i)
			}
		}
	}
	if got := w.LinesFor(acc.Key, now); len(got) != 0 {
		t.Fatalf("new account has history: %d lines", len(got))
	}
	if got := w.LinesFor(acc.Key, now.Add(60*24*time.Hour)); len(got) == 0 {
		t.Fatal("new account never moves")
	}
	if _, err := w.OpenAccount("tunde", "bank-b", "current", now); err == nil {
		t.Fatal("second current account at the same bank was allowed")
	}
	if _, err := w.OpenAccount("nobody", "bank-b", "current", now); err == nil {
		t.Fatal("unknown persona was allowed")
	}
	w.Suspend("bank-a", now)
	if _, err := w.OpenAccount("ngozi", "bank-a", "current", now.Add(time.Hour)); !errors.Is(err, ErrBankSuspended) {
		t.Fatalf("opening at a suspended bank: %v", err)
	}
	again := New(w.Config(), w.Overrides())
	if _, ok := again.Account(acc.Key); !ok {
		t.Fatal("opened account is not rebuilt from overrides")
	}
	horizon := now.Add(60 * 24 * time.Hour)
	if len(again.LinesFor(acc.Key, horizon)) != len(w.LinesFor(acc.Key, horizon)) {
		t.Fatal("opened account is not rebuilt the same")
	}
}
