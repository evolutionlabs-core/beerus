package cbn

import (
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/banktest"
	"github.com/evolutionlabs-core/beerus/internal/clock"
)

func TestBooksLedgerRules(t *testing.T) {
	ctx := t.Context()
	pool := banktest.Schema(t)
	clk := clock.New()
	b, err := New(ctx, pool, clk)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := b.OpenAccount(ctx, "bank-a", 1000, 200)
	if err != nil {
		t.Fatal(err)
	}
	first := opened.OpenedAt
	cutoff := time.Date(2026, time.November, 12, 12, 0, 0, 0, clock.Lagos)
	later, err := New(ctx, pool, clk)
	if err != nil {
		t.Fatal(err)
	}
	waitPast(t, clk, first)
	nets := func(a, c int64) []Net { return []Net{{"bank-a", a}, {"bank-b", c}} }
	open := []struct {
		name       string
		books      *Books
		bank       string
		settlement int64
		vault      int64
		want       Account
		err        string
	}{
		{"open a", b, "bank-a", 1000, 200, Account{"bank-a", 1000, 200, first}, ""},
		{"open a again at another time", later, "bank-a", 1000, 200, Account{"bank-a", 1000, 200, first}, ""},
		{"open b without vault cash", b, "bank-b", 1000, 0, Account{"bank-b", 1000, 0, time.Time{}}, ""},
		{"open a with other amounts", b, "bank-a", 999, 200, Account{}, "bank-a opened with settlement_minor 1000 and vault_minor 200, and this request has settlement_minor 999. Send the amounts it opened with."},
		{"open with nothing", b, "bank-c", 0, 0, Account{}, "Send a settlement_minor and a vault_minor of zero or more, with one of them above zero, that fit together."},
		{"open too large", b, "bank-c", math.MaxInt64, 1, Account{}, "Send a settlement_minor and a vault_minor of zero or more, with one of them above zero, that fit together."},
	}
	for _, c := range open {
		before := clk.Now()
		got, err := c.books.OpenAccount(ctx, c.bank, c.settlement, c.vault)
		after := clk.Now()
		openedAtWrong := !c.want.OpenedAt.IsZero() && !got.OpenedAt.Equal(c.want.OpenedAt) || c.want.OpenedAt.IsZero() && (got.OpenedAt.Before(before) || got.OpenedAt.After(after))
		switch {
		case c.err == "" && (err != nil || got.BankID != c.want.BankID || got.Settlement != c.want.Settlement || got.Vault != c.want.Vault || openedAtWrong):
			t.Errorf("%s: %+v, %v, want %+v", c.name, got, err, c.want)
		case c.err != "" && (err == nil || err.Error() != c.err):
			t.Errorf("%s: %v, want %q", c.name, err, c.err)
		}
	}
	post := []struct {
		name     string
		id       string
		nets     []Net
		replayed bool
		err      string
	}{
		{"post", "s1", nets(-200, 200), false, ""},
		{"post again", "s1", nets(-200, 200), true, ""},
		{"post again with a zero net added", "s1", append(nets(-200, 200), Net{"bank-c", 0}), true, ""},
		{"post again with other nets", "s1", nets(-300, 300), false, "Session s1 was posted with bank-a -200 and bank-b 200, and this request has bank-a -300 and bank-b 300. Send a new session_id, or repeat the first nets."},
		{"unbalanced", "s2", nets(-100, 250), false, "The session nets to 150, not zero. Send nets that sum to zero."},
		{"unknown bank", "s3", []Net{{"bank-a", -10}, {"bank-x", 10}}, false, "bank-x has no settlement account at the CBN. Open it first."},
		{"one bank twice", "s4", []Net{{"bank-a", -10}, {"bank-a", 10}, {"bank-b", 0}}, false, "bank-a appears 2 times in session s4. Send one net per bank."},
		{"nets too large", "s5", []Net{{"bank-a", math.MaxInt64}, {"bank-b", 1}}, false, "The nets of session s5 are too large to add up. The largest net is 9223372036854775807 kobo. Send smaller nets."},
		{"balance past the largest amount", "s5b", []Net{{"bank-a", -math.MaxInt64}, {"bank-b", math.MaxInt64}}, false, "Session s5b would take bank-b's settlement balance past the largest amount: 1200 + 9223372036854775807. Send smaller nets."},
		{"one net", "s6", []Net{{"bank-a", 0}}, false, "Send a session_id, a cutoff_at and at least 2 nets."},
		{"nothing moves", "s7", nets(0, 0), false, ""},
	}
	for _, c := range post {
		got, replayed, err := b.Post(ctx, c.id, cutoff, c.nets)
		switch {
		case c.err == "" && (err != nil || replayed != c.replayed || got.Key != "session:"+c.id):
			t.Errorf("%s: %+v, %v, %v", c.name, got, replayed, err)
		case c.err != "" && (err == nil || err.Error() != c.err):
			t.Errorf("%s: %v, want %q", c.name, err, c.err)
		}
	}
	posted := newestPosted(t, b)
	if bal, err := b.Balance(ctx, "bank-a"); err != nil || bal != 800 {
		t.Fatalf("balance of bank-a: %d, %v", bal, err)
	}
	if bal, err := b.Balance(ctx, "bank-x"); !errors.Is(err, ErrNoAccount) || bal != 0 || err.Error() != "read balance of bank-x: no settlement account at the CBN" {
		t.Fatalf("balance of an unknown bank: %d, %v", bal, err)
	}
	list, next, err := b.Postings(ctx, "bank-a", 1, "")
	want := Posting{Key: "session:s1", Kind: "session", SessionID: "s1", Narration: "NIP settlement session s1", PostedAt: posted, Nets: nets(-200, 200)}
	if err != nil || next != "session:s1" || len(list) != 1 || !list[0].PostedAt.Equal(posted) || !reflect.DeepEqual(list[0].Nets, want.Nets) || list[0].Key != want.Key {
		t.Fatalf("first page: %+v %q %v", list, next, err)
	}
	waitPast(t, clk, posted)
	if replay, replayed, err := later.Post(ctx, "s1", cutoff, nets(-200, 200)); err != nil || !replayed || !replay.PostedAt.Equal(posted) {
		t.Fatalf("replay at a later time: %+v, %v, %v", replay, replayed, err)
	}
	rest, next, err := b.Postings(ctx, "bank-a", 5, next)
	if err != nil || next != "" || len(rest) != 1 || rest[0].Key != "genesis:bank-a" || rest[0].Kind != "genesis" || rest[0].SessionID != "" || !reflect.DeepEqual(rest[0].Nets, []Net{{"bank-a", 1000}}) {
		t.Fatalf("second page: %+v %q %v", rest, next, err)
	}
	if _, _, err := b.Postings(ctx, "", 5, "session:nope"); !errors.Is(err, ErrCursor) {
		t.Fatalf("a cursor that is not a posting: %v", err)
	}
	problems, err := b.Problems(ctx)
	if err != nil || len(problems) != 0 {
		t.Fatalf("problems of balanced books: %v, %v", problems, err)
	}
}

func newestPosted(t *testing.T, b *Books) time.Time {
	t.Helper()
	list, _, err := b.Postings(t.Context(), "bank-a", 1, "")
	if err != nil || len(list) != 1 {
		t.Fatalf("first posting: %+v %v", list, err)
	}
	return list[0].PostedAt
}

func waitPast(t *testing.T, clk *clock.Clock, at time.Time) {
	t.Helper()
	deadline := clk.Real().Add(time.Second)
	for !clk.Now().After(at) {
		if clk.Real().After(deadline) {
			t.Fatal("the clock did not move")
		}
	}
}
