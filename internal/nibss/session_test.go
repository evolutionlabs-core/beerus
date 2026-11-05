package nibss

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

var t0 = time.Date(2026, 11, 4, 9, 12, 0, 0, clock.Lagos)

func sessionStore(t *testing.T) *Store {
	t.Helper()
	s := testStore(t)
	register(t, s, "bank-a", "Bank A", "999001", "901")
	register(t, s, "bank-b", "Bank B", "999002", "902")
	return s
}

func transfer(id, to string, at time.Time) Session {
	return Session{ID: id, FromCode: "999001", ToCode: to, FromNUBAN: world.NUBAN("901", 1), ToNUBAN: world.NUBAN("902", 2), Amount: 500000, Narration: "rent", CreatedAt: at}
}

func ptr(t time.Time) *time.Time { return &t }

func TestSessionSuccess(t *testing.T) {
	ctx := context.Background()
	s := sessionStore(t)
	x := transfer("s1", "999002", t0)
	x.NextTSQAt = ptr(t0.Add(2 * time.Minute))
	got, err := s.Begin(ctx, x)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != SessionPending || got.Outcome != "" || got.ResolvedAt != nil || !got.CreatedAt.Equal(t0) || !got.NextTSQAt.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("begin: %+v", got)
	}
	again, err := s.Begin(ctx, transfer("s1", "999002", t0.Add(time.Hour)))
	if err != nil || again.Status != SessionPending || !again.CreatedAt.Equal(t0) {
		t.Fatalf("resend: %+v, %v", again, err)
	}
	changed := transfer("s1", "999002", t0)
	changed.Amount++
	_, err = s.Begin(ctx, changed)
	wantErr(t, err, "session s1: session id reused with different details")

	q, err := s.TSQ(ctx, "s1", t0.Add(4*time.Minute))
	if err != nil || q.TSQAttempts != 1 || q.Status != SessionPending || !q.NextTSQAt.Equal(t0.Add(4*time.Minute)) {
		t.Fatalf("tsq: %+v, %v", q, err)
	}
	if err := s.Resolve(ctx, "s1", SessionOK, OutcomeCredited, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(ctx, "s1", SessionOK, OutcomeCredited, t0.Add(time.Hour)); err != nil {
		t.Fatalf("repeat resolve: %v", err)
	}
	err = s.Resolve(ctx, "s1", "91", OutcomeFailed, t0.Add(time.Hour))
	wantErr(t, err, "session s1 is 00/credited and cannot become 91/failed: status change not allowed")
	q, err = s.TSQ(ctx, "s1", t0.Add(8*time.Minute))
	if err != nil || q.TSQAttempts != 1 || q.Status != SessionOK || q.Outcome != OutcomeCredited || !q.ResolvedAt.Equal(t0.Add(time.Minute)) || q.NextTSQAt != nil {
		t.Fatalf("tsq after resolve: %+v, %v", q, err)
	}
}

func TestSessionFailureThenReversal(t *testing.T) {
	ctx := context.Background()
	s := sessionStore(t)
	if _, err := s.Begin(ctx, transfer("s2", "999002", t0)); err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(ctx, "s2", "91", OutcomeFailed, t0); err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(ctx, "s2", "91", OutcomeReversed, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	q, _ := s.TSQ(ctx, "s2", t0)
	if q.Status != "91" || q.Outcome != OutcomeReversed || !q.ResolvedAt.Equal(t0) {
		t.Fatalf("after reversal: %+v", q)
	}
	err := s.Resolve(ctx, "s2", "91", OutcomeFailed, t0)
	wantErr(t, err, "session s2 is 91/reversed and cannot become 91/failed: status change not allowed")
}

func TestResolveRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	s := sessionStore(t)
	if _, err := s.Begin(ctx, transfer("s3", "999002", t0)); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		status, outcome string
		at              time.Time
		want            string
	}{
		{SessionPending, OutcomeUnknown, t0, `session s3: "pending" is not a response code`},
		{"9", OutcomeFailed, t0, `session s3: "9" is not a response code`},
		{SessionOK, OutcomeFailed, t0, `session s3: a 00 session is credited, not "failed"`},
		{"91", OutcomeCredited, t0, `session s3: a 91 session is failed or reversed, not "credited"`},
		{"91", OutcomeFailed, time.Time{}, "session s3 needs a resolved time"},
		{SessionOK, OutcomeCredited, t0.Add(-time.Second), "session s3 cannot resolve at 2026-11-04T09:11:59+01:00, before it began at 2026-11-04T09:12:00+01:00"},
	}
	for _, c := range cases {
		wantErr(t, s.Resolve(ctx, "s3", c.status, c.outcome, c.at), c.want)
	}
	err := s.Resolve(ctx, "nope", SessionOK, OutcomeCredited, t0)
	if !errors.Is(err, ErrNoRecord) {
		t.Fatalf("got %v, want ErrNoRecord", err)
	}
	_, err = s.TSQ(ctx, "s3", time.Time{})
	wantErr(t, err, "session s3 needs the next status query time")
	_, err = s.TSQ(ctx, "nope", t0)
	if !errors.Is(err, ErrNoRecord) {
		t.Fatalf("got %v, want ErrNoRecord", err)
	}
}

func TestBeginRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	s := sessionStore(t)
	mod := func(f func(*Session)) Session {
		x := transfer("s4", "999002", t0)
		f(&x)
		return x
	}
	cases := []struct {
		x    Session
		want string
	}{
		{mod(func(x *Session) { x.ID = "" }), "a session needs an id"},
		{mod(func(x *Session) { x.ToNUBAN = "123" }), "session s4: account numbers have 10 digits: 07 invalid account"},
		{mod(func(x *Session) { x.Amount = 0 }), "session s4: amount must be above zero, not 0"},
		{mod(func(x *Session) { x.CreatedAt = time.Time{} }), "session s4 needs a created time"},
		{mod(func(x *Session) { x.Status = SessionOK }), "session s4 starts pending, not 00"},
		{mod(func(x *Session) { x.Outcome = OutcomeReversed }), "session s4 cannot start with outcome reversed"},
		{mod(func(x *Session) { x.Outcome = OutcomeUnknown }), "session s4 holds outcome unknown and needs a status query time"},
		{mod(func(x *Session) { x.Settlement = "c1" }), "session s4 must start unresolved, unsettled and with no status queries"},
		{mod(func(x *Session) { x.ToCode = "123456" }), "bank code 123456 is not in the NIBSS directory"},
		{mod(func(x *Session) { x.FromCode = "999003" }), "bank code 999003 is not in the NIBSS directory"},
	}
	for _, c := range cases {
		_, err := s.Begin(ctx, c.x)
		wantErr(t, err, c.want)
	}
}

func TestBeginStatusGates(t *testing.T) {
	ctx := context.Background()
	s := sessionStore(t)
	if err := s.SetStatus(ctx, "bank-b", StatusSuspended); err != nil {
		t.Fatal(err)
	}
	_, err := s.Begin(ctx, transfer("g1", "999002", t0))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("to suspended: %v", err)
	}
	wantErr(t, err, "Bank B is suspended: 91 institution unavailable")
	if err := s.SetStatus(ctx, "bank-b", StatusActive); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStatus(ctx, "bank-a", StatusTerminating); err != nil {
		t.Fatal(err)
	}
	_, err = s.Begin(ctx, transfer("g2", "999002", t0))
	wantErr(t, err, "Bank A is terminating: 91 institution unavailable")
	closing := transfer("g3", "999002", t0)
	closing.Closure = true
	got, err := s.Begin(ctx, closing)
	if err != nil || !got.Closure || got.Status != SessionPending {
		t.Fatalf("closure from terminating: %+v, %v", got, err)
	}
	if err := s.SetStatus(ctx, "bank-a", StatusRemoved); err != nil {
		t.Fatal(err)
	}
	closing.ID = "g4"
	_, err = s.Begin(ctx, closing)
	wantErr(t, err, "Bank A is removed: 91 institution unavailable")
	if err := s.SetStatus(ctx, "bank-b", StatusTerminating); err != nil {
		t.Fatal(err)
	}
	back := transfer("g5", "999001", t0)
	back.FromCode, back.Closure = "999002", true
	_, err = s.Begin(ctx, back)
	wantErr(t, err, "Bank A is removed: 91 institution unavailable")
}

func TestDueOrder(t *testing.T) {
	ctx := context.Background()
	s := sessionStore(t)
	begin := func(id string, created time.Time, next *time.Time, outcome string) {
		x := transfer(id, "999002", created)
		x.NextTSQAt, x.Outcome = next, outcome
		if _, err := s.Begin(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	begin("d-late", t0, ptr(t0.Add(3*time.Minute)), OutcomeUnknown)
	begin("d-b", t0.Add(time.Second), ptr(t0.Add(2*time.Minute)), OutcomeCredited)
	begin("d-a", t0.Add(time.Second), ptr(t0.Add(2*time.Minute)), OutcomeFailed)
	begin("d-first", t0.Add(2*time.Second), ptr(t0.Add(time.Minute)), OutcomeUnknown)
	begin("d-none", t0, nil, "")
	begin("d-done", t0, ptr(t0), OutcomeCredited)
	if err := s.Resolve(ctx, "d-done", SessionOK, OutcomeCredited, t0); err != nil {
		t.Fatal(err)
	}
	ids := func(at time.Time) []string {
		due, err := s.Due(ctx, at)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, x := range due {
			out = append(out, x.ID)
		}
		return out
	}
	check := func(at time.Time, want ...string) {
		t.Helper()
		got := ids(at)
		if len(got) != len(want) {
			t.Fatalf("due at %s = %v, want %v", at.Format(time.Kitchen), got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("due at %s = %v, want %v", at.Format(time.Kitchen), got, want)
			}
		}
	}
	check(t0)
	check(t0.Add(time.Minute), "d-first")
	check(t0.Add(2*time.Minute), "d-first", "d-a", "d-b")
	check(t0.Add(time.Hour), "d-first", "d-a", "d-b", "d-late")
	q, err := s.TSQ(ctx, "d-b", t0.Add(10*time.Minute))
	if err != nil || q.Outcome != OutcomeCredited || q.TSQAttempts != 1 || !q.NextTSQAt.Equal(t0.Add(10*time.Minute)) {
		t.Fatalf("tsq keeps stored outcome: %+v, %v", q, err)
	}
	check(t0.Add(2*time.Minute), "d-first", "d-a")
	check(t0.Add(time.Hour), "d-first", "d-a", "d-late", "d-b")
}

func TestSettlement(t *testing.T) {
	ctx := context.Background()
	s := sessionStore(t)
	for i, id := range []string{"u1", "u2", "u3", "u4"} {
		if _, err := s.Begin(ctx, transfer(id, "999002", t0)); err != nil {
			t.Fatal(err)
		}
		if id == "u4" {
			continue
		}
		if err := s.Resolve(ctx, id, SessionOK, OutcomeCredited, t0.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	un, err := s.Unsettled(ctx, t0.Add(time.Hour))
	if err != nil || len(un) != 2 || un[0].ID != "u1" || un[1].ID != "u2" {
		t.Fatalf("unsettled at cutoff: %+v, %v", un, err)
	}
	if err := s.MarkSettled(ctx, []string{"u2", "u1", "u1"}, "c1"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSettled(ctx, []string{"u1"}, "c1"); err != nil {
		t.Fatalf("repeat mark: %v", err)
	}
	wantErr(t, s.MarkSettled(ctx, []string{"u1", "u3"}, "c2"), "1 of 2 sessions are already settled in another session")
	wantErr(t, s.MarkSettled(ctx, []string{"u3", "u4"}, "c2"), "1 of 2 sessions to settle are not 00")
	err = s.MarkSettled(ctx, []string{"u3", "x1", "x2"}, "c2")
	wantErr(t, err, "2 of 3 sessions to settle do not exist: 25 no record")
	wantErr(t, s.MarkSettled(ctx, nil, "c2"), "mark settled needs at least one session id")
	wantErr(t, s.MarkSettled(ctx, []string{"u3"}, ""), "mark settled needs a settlement session")
	un, _ = s.Unsettled(ctx, t0.Add(24*time.Hour))
	if len(un) != 1 || un[0].ID != "u3" {
		t.Fatalf("after settling: %+v", un)
	}
	q, _ := s.TSQ(ctx, "u1", t0)
	if q.Settlement != "c1" {
		t.Errorf("u1 settlement %q, want c1", q.Settlement)
	}
}

func TestNameEnquiry(t *testing.T) {
	ctx := context.Background()
	s := sessionStore(t)
	good := world.NUBAN("902", 1234567)
	bad := good[:9] + string('0'+(good[9]-'0'+1)%10)
	cases := []struct {
		code, nuban string
		is          error
		want        string
	}{
		{"999002", good, ErrAskBank, "Bank B: the account name comes from the receiving bank"},
		{"999002", bad, ErrInvalidAccount, bad + " is not a valid account number at Bank B: 07 invalid account"},
		{"999002", "12345", ErrInvalidAccount, "an account number has 10 digits: 07 invalid account"},
		{"999123", good, ErrUnknownInstitution, "bank code 999123 is not in the NIBSS directory"},
	}
	for _, c := range cases {
		_, err := s.NameEnquiry(ctx, c.code, c.nuban)
		if !errors.Is(err, c.is) {
			t.Errorf("%s/%s: %v, want %v", c.code, c.nuban, err, c.is)
		}
		wantErr(t, err, c.want)
	}
	if err := s.SetStatus(ctx, "bank-b", StatusTerminating); err != nil {
		t.Fatal(err)
	}
	_, err := s.NameEnquiry(ctx, "999002", good)
	wantErr(t, err, "Bank B is terminating: 91 institution unavailable")
}
