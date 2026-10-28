package ledger_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/banktest"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/clock"
)

var at = time.Date(2026, 3, 2, 10, 0, 0, 0, clock.Lagos)

func setup(t *testing.T) (*ledger.Ledger, context.Context) {
	ctx := context.Background()
	l := ledger.New(banktest.Schema(t), ledger.Limits{1: 5000000, 3: 1 << 50})
	for _, a := range []ledger.Account{
		{Code: "CASH", Name: "Cash", Kind: ledger.Asset},
		{Code: "INCOME", Name: "Income", Kind: ledger.Income},
		{Code: "C1", Name: "One", Kind: ledger.Liability, Customer: true, NUBAN: "0000000011", KYCTier: 3},
		{Code: "C2", Name: "Two", Kind: ledger.Liability, Customer: true, NUBAN: "0000000022", KYCTier: 1},
	} {
		if _, err := l.OpenAccount(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	return l, ctx
}

func e(key string, lines ...ledger.Line) ledger.Entry {
	d := clock.DateOf(at)
	return ledger.Entry{Key: key, Kind: "test", PostingDate: d, PostedAt: at, Narration: key, Lines: lines}
}

func ln(acct string, amt int64) ledger.Line {
	return ledger.Line{Account: acct, Amount: amt}
}

func verify(t *testing.T, l *ledger.Ledger) {
	t.Helper()
	c, err := l.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !c.OK() {
		t.Fatal(c)
	}
}

func TestPostingAndBalances(t *testing.T) {
	l, ctx := setup(t)
	if _, err := l.Post(ctx, e("dep", ln("CASH", 100000), ln("C1", -100000))); err != nil {
		t.Fatal(err)
	}
	if b, _ := l.Balance(ctx, "C1"); b != 100000 {
		t.Fatalf("customer balance %d", b)
	}
	if b, _ := l.Balance(ctx, "CASH"); b != 100000 {
		t.Fatalf("cash balance %d", b)
	}
	evs, err := l.Events(ctx, "C1", 0, 10)
	if err != nil || len(evs) != 1 || evs[0].Amount != 100000 || evs[0].BalanceAfter != 100000 {
		t.Fatalf("events %v %v", evs, err)
	}
	if _, err := l.Post(ctx, e("bad", ln("CASH", 1), ln("C1", -2))); !errors.Is(err, ledger.ErrUnbalanced) {
		t.Fatalf("unbalanced: %v", err)
	}
	if _, err := l.Post(ctx, e("one", ln("CASH", 1))); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatalf("one line: %v", err)
	}
	if _, err := l.Post(ctx, e("nope", ln("CASH", 1), ln("MISSING", -1))); !errors.Is(err, ledger.ErrUnknownAccount) {
		t.Fatalf("unknown account: %v", err)
	}
	verify(t, l)
}

func TestAppendOnlyAndDeferredBalanceCheck(t *testing.T) {
	l, ctx := setup(t)
	if _, err := l.Post(ctx, e("dep", ln("CASH", 500), ln("C1", -500))); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`UPDATE journal_lines SET amount = 1`, `DELETE FROM journal_entries`, `UPDATE account_events SET amount = 0`} {
		if _, err := l.Pool().Exec(ctx, q); err == nil {
			t.Fatalf("%s was allowed", q)
		}
	}
	tx, err := l.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO journal_entries (idempotency_key, request_hash, kind, posting_date, value_date, posted_at, narration) VALUES ('raw', 'h', 'raw', now(), now(), now(), 'raw') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO journal_lines (entry_id, line_no, account_id, amount, currency, narration) SELECT $1, 1, id, 7, 'NGN', 'x' FROM gl_accounts WHERE code = 'CASH'`, id); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err == nil {
		t.Fatal("an unbalanced entry committed")
	}
	verify(t, l)
}

func TestIdempotentPosting(t *testing.T) {
	l, ctx := setup(t)
	a, err := l.Post(ctx, e("k1", ln("CASH", 100), ln("C1", -100)))
	if err != nil {
		t.Fatal(err)
	}
	b, err := l.Post(ctx, e("k1", ln("CASH", 100), ln("C1", -100)))
	if err != nil || !b.Replayed || b.EntryID != a.EntryID {
		t.Fatalf("replay %+v %v", b, err)
	}
	if _, err := l.Post(ctx, e("k1", ln("CASH", 200), ln("C1", -200))); !errors.Is(err, ledger.ErrIdempotency) {
		t.Fatalf("changed body: %v", err)
	}
	if bal, _ := l.Balance(ctx, "C1"); bal != 100 {
		t.Fatalf("balance %d after replay", bal)
	}
	verify(t, l)
}

func TestFundsStatusesAndLimits(t *testing.T) {
	l, ctx := setup(t)
	if _, err := l.Post(ctx, e("dep1", ln("CASH", 10000000), ln("C1", -10000000))); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Post(ctx, e("dep2", ln("CASH", 10000000), ln("C2", -10000000))); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Post(ctx, e("over", ln("C1", 10000001), ln("CASH", -10000001))); !errors.Is(err, ledger.ErrInsufficientFunds) {
		t.Fatalf("overdraw: %v", err)
	}
	tr := e("t1", ln("C2", 3000000), ln("C1", -3000000))
	tr.Kind, tr.LimitedAmount = "transfer", 3000000
	if _, err := l.Post(ctx, tr); err != nil {
		t.Fatal(err)
	}
	tr2 := e("t2", ln("C2", 2500000), ln("C1", -2500000))
	tr2.Kind, tr2.LimitedAmount = "transfer", 2500000
	if _, err := l.Post(ctx, tr2); !errors.Is(err, ledger.ErrDailyLimit) {
		t.Fatalf("tier 1 daily limit: %v", err)
	}
	if err := l.SetStatus(ctx, "C1", "pnd"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Post(ctx, e("pnd-debit", ln("C1", 1), ln("CASH", -1))); !errors.Is(err, ledger.ErrPostNoDebit) {
		t.Fatalf("pnd debit: %v", err)
	}
	if _, err := l.Post(ctx, e("pnd-credit", ln("CASH", 1), ln("C1", -1))); err != nil {
		t.Fatalf("pnd credit: %v", err)
	}
	if err := l.SetStatus(ctx, "C1", "frozen"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Post(ctx, e("frozen-credit", ln("CASH", 1), ln("C1", -1))); !errors.Is(err, ledger.ErrAccountFrozen) {
		t.Fatalf("frozen credit: %v", err)
	}
	if err := l.SetStatus(ctx, "C1", "closed"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Post(ctx, e("closed", ln("CASH", 1), ln("C1", -1))); !errors.Is(err, ledger.ErrAccountClosed) {
		t.Fatalf("closed: %v", err)
	}
	verify(t, l)
}

func TestReverseOnce(t *testing.T) {
	l, ctx := setup(t)
	if _, err := l.Post(ctx, e("dep", ln("CASH", 5000), ln("C1", -5000))); err != nil {
		t.Fatal(err)
	}
	p, err := l.Post(ctx, e("fee", ln("C1", 50), ln("INCOME", -50)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Reverse(ctx, p.EntryID, "rev1", at.Add(time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Reverse(ctx, p.EntryID, "rev2", at.Add(2*time.Hour), ""); !errors.Is(err, ledger.ErrAlreadyReversed) {
		t.Fatalf("second reversal: %v", err)
	}
	if b, _ := l.Balance(ctx, "C1"); b != 5000 {
		t.Fatalf("balance after reversal %d", b)
	}
	evs, _ := l.Events(ctx, "C1", 0, 10)
	if len(evs) != 3 || evs[2].Narration[:4] != "REV/" || evs[2].Amount != 50 {
		t.Fatalf("reversal is not a separate statement line: %+v", evs)
	}
	verify(t, l)
}
