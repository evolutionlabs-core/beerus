package nibss

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

var t0 = time.Date(2026, 11, 4, 9, 12, 0, 0, clock.Lagos)

func sessionStore(t *testing.T) *Store {
	t.Helper()
	s := testStore(t)
	registerCapped(t, s, "bank-a", "Bank A", "999001", "901", Limits{NetDebitCap: 100_000_000})
	registerCapped(t, s, "bank-b", "Bank B", "999002", "902", Limits{NetDebitCap: 100_000_000})
	return s
}

func begin(ctx context.Context, s *Store, x Session) (Session, error) {
	got, _, err := s.Begin(ctx, x)
	return got, err
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
	got, err := begin(ctx, s, x)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != SessionPending || got.Outcome != "" || got.ResolvedAt != nil || !got.CreatedAt.Equal(t0) || !got.NextTSQAt.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("begin: %+v", got)
	}
	again, err := begin(ctx, s, transfer("s1", "999002", t0.Add(time.Hour)))
	if err != nil || again.Status != SessionPending || !again.CreatedAt.Equal(t0) {
		t.Fatalf("resend: %+v, %v", again, err)
	}
	changed := transfer("s1", "999002", t0)
	changed.Amount++
	_, err = begin(ctx, s, changed)
	wantErr(t, err, "session s1: session id reused with different amount_minor")

	q, err := s.TSQ(ctx, "s1", t0.Add(4*time.Minute))
	if err != nil || q.TSQAttempts != 1 || q.Status != SessionPending || !q.NextTSQAt.Equal(t0.Add(4*time.Minute)) {
		t.Fatalf("tsq: %+v, %v", q, err)
	}
	if err := s.Resolve(ctx, "s1", SessionOK, nip.OutcomeCredited, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(ctx, "s1", SessionOK, nip.OutcomeCredited, t0.Add(time.Hour)); err != nil {
		t.Fatalf("repeat resolve: %v", err)
	}
	err = s.Resolve(ctx, "s1", "91", nip.OutcomeFailed, t0.Add(time.Hour))
	wantErr(t, err, "session s1 is 00/credited and cannot become 91/failed: status change not allowed")
	q, err = s.TSQ(ctx, "s1", t0.Add(8*time.Minute))
	if err != nil || q.TSQAttempts != 1 || q.Status != SessionOK || q.Outcome != nip.OutcomeCredited || !q.ResolvedAt.Equal(t0.Add(time.Minute)) || q.NextTSQAt != nil {
		t.Fatalf("tsq after resolve: %+v, %v", q, err)
	}
}

func TestSessionFailure(t *testing.T) {
	ctx := context.Background()
	s := sessionStore(t)
	if _, err := begin(ctx, s, transfer("s2", "999002", t0)); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.Resolve(ctx, "s2", "91", nip.OutcomeFailed, t0); err != nil {
			t.Fatal(err)
		}
	}
	q, _ := s.TSQ(ctx, "s2", t0)
	if q.Status != "91" || q.Outcome != nip.OutcomeFailed || !q.ResolvedAt.Equal(t0) {
		t.Fatalf("after failure: %+v", q)
	}
	err := s.Resolve(ctx, "s2", SessionOK, nip.OutcomeCredited, t0)
	wantErr(t, err, "session s2 is 91/failed and cannot become 00/credited: status change not allowed")
}

func TestResolveRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	s := sessionStore(t)
	if _, err := begin(ctx, s, transfer("s3", "999002", t0)); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		status, outcome string
		at              time.Time
		want            string
	}{
		{SessionPending, nip.OutcomeUnknown, t0, `session s3: "pending" is not a response code`},
		{"9", nip.OutcomeFailed, t0, `session s3: "9" is not a response code`},
		{SessionOK, nip.OutcomeFailed, t0, `session s3: a 00 session is credited, not "failed"`},
		{"91", nip.OutcomeCredited, t0, `session s3: a 91 session is failed, not "credited"`},
		{"91", nip.OutcomeFailed, time.Time{}, "session s3 needs a resolved time"},
		{SessionOK, nip.OutcomeCredited, t0.Add(-time.Second), "session s3 cannot resolve at 2026-11-04T09:11:59+01:00, before it began at 2026-11-04T09:12:00+01:00"},
	}
	for _, c := range cases {
		wantErr(t, s.Resolve(ctx, "s3", c.status, c.outcome, c.at), c.want)
	}
	err := s.Resolve(ctx, "nope", SessionOK, nip.OutcomeCredited, t0)
	if !errors.Is(err, nip.ErrNoRecord) {
		t.Fatalf("got %v, want nip.ErrNoRecord", err)
	}
	_, err = s.TSQ(ctx, "s3", time.Time{})
	wantErr(t, err, "session s3 needs the next status query time")
	_, err = s.TSQ(ctx, "nope", t0)
	if !errors.Is(err, nip.ErrNoRecord) {
		t.Fatalf("got %v, want nip.ErrNoRecord", err)
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
		{mod(func(x *Session) { x.Outcome = "reversed" }), "session s4 cannot start with outcome reversed"},
		{mod(func(x *Session) { x.Outcome = nip.OutcomeUnknown }), "session s4 holds outcome unknown and needs a status query time"},
		{mod(func(x *Session) { x.Settlement = "c1" }), "session s4 must start unresolved, unsettled and with no status queries"},
		{mod(func(x *Session) { x.ToCode = "123456" }), "bank code 123456 is not in the NIBSS directory"},
		{mod(func(x *Session) { x.FromCode = "999003" }), "bank code 999003 is not in the NIBSS directory"},
	}
	for _, c := range cases {
		_, err := begin(ctx, s, c.x)
		wantErr(t, err, c.want)
	}
}

func TestBeginStatusGates(t *testing.T) {
	ctx := context.Background()
	s := sessionStore(t)
	rows := []struct {
		name    string
		prepare func()
		id      string
		from    string
		to      string
		closure bool
		status  string
		outcome string
		err     string
	}{
		{name: "to suspended", prepare: func() { setStatus(t, s, "bank-b", nip.StatusSuspended) }, id: "g1", from: "999001", to: "999002", status: "91", outcome: "failed"},
		{name: "both active", prepare: func() { setStatus(t, s, "bank-b", nip.StatusActive) }, id: "g2", from: "999001", to: "999002", status: SessionPending},
		{name: "closure from active", id: "g3", from: "999001", to: "999002", closure: true, err: "Bank A is active, so it cannot send a closure transfer"},
		{name: "from terminating", prepare: func() { setStatus(t, s, "bank-a", nip.StatusTerminating) }, id: "g4", from: "999001", to: "999002", status: "91", outcome: "failed"},
		{name: "closure from terminating", id: "g5", from: "999001", to: "999002", closure: true, status: SessionPending},
		{name: "closure to terminating", prepare: func() { setStatus(t, s, "bank-b", nip.StatusTerminating) }, id: "g6", from: "999001", to: "999002", closure: true, status: "91", outcome: "failed"},
		{name: "from removed", prepare: func() { setStatus(t, s, "bank-a", nip.StatusRemoved) }, id: "g7", from: "999001", to: "999002", closure: true, err: "Bank A is removed, so it cannot send a closure transfer"},
	}
	for _, r := range rows {
		if r.prepare != nil {
			r.prepare()
		}
		x := transfer(r.id, r.to, t0)
		x.FromCode, x.Closure = r.from, r.closure
		got, err := begin(ctx, s, x)
		if r.err != "" {
			var closure *ClosureError
			if !errors.As(err, &closure) || err.Error() != r.err {
				t.Errorf("%s: got %v, want %s", r.name, err, r.err)
			}
			continue
		}
		if err != nil || got.Status != r.status || got.Outcome != r.outcome || (r.status != SessionPending) != (got.ResolvedAt != nil) {
			t.Errorf("%s: %+v, %v", r.name, got, err)
		}
		if r.status == "91" {
			again, err := s.Session(ctx, r.id)
			if err != nil || again.Status != "91" || again.Outcome != nip.OutcomeFailed {
				t.Errorf("%s: stored %+v, %v", r.name, again, err)
			}
		}
	}
}

func setStatus(t *testing.T, s *Store, id, status string) {
	t.Helper()
	if err := s.SetStatus(context.Background(), id, status); err != nil {
		t.Fatal(err)
	}
}

func TestBeginCapReplayAndNoAnswer(t *testing.T) {
	ctx := context.Background()
	s := sessionStore(t)
	registerCapped(t, s, "bank-a", "Bank A", "999001", "901", Limits{NetDebitCap: 1_200_000, Collateral: 5})
	rows := []struct {
		id       string
		amount   int64
		closure  bool
		status   string
		replayed bool
	}{
		{"c1", 500_000, false, SessionPending, false},
		{"c2", 700_000, false, SessionPending, false},
		{"c3", 1, false, "61", false},
		{"c3", 1, false, "61", true},
		{"c4", 5_000_000, false, "61", false},
		{"c5", math.MaxInt64, false, "61", false},
	}
	for _, r := range rows {
		x := transfer(r.id, "999002", t0)
		x.Amount, x.Closure = r.amount, r.closure
		got, replayed, err := s.Begin(ctx, x)
		if err != nil || got.Status != r.status || replayed != r.replayed {
			t.Fatalf("%s: %+v replayed %v, %v", r.id, got, replayed, err)
		}
	}
	pos, err := s.Position(ctx, "999001")
	if err != nil || pos.Outward != 1_200_000 || pos.Inward != 0 {
		t.Fatalf("position of the sender: %+v, %v", pos, err)
	}
	if pos, err = s.Position(ctx, "999002"); err != nil || pos.Inward != 1_200_000 || pos.NetDebit() != 0 {
		t.Fatalf("position of the receiver: %+v, %v", pos, err)
	}
	changed := transfer("c1", "999002", t0)
	changed.Amount, changed.Narration = 1, "other"
	_, _, err = s.Begin(ctx, changed)
	var reused *ReusedError
	if !errors.As(err, &reused) || len(reused.Fields) != 2 || reused.Fields[0] != (FieldDiff{"amount_minor", "500000", "1"}) || reused.Fields[1] != (FieldDiff{"narration", `"rent"`, `"other"`}) {
		t.Fatalf("reused: %v", err)
	}

	next := t0.Add(2 * time.Minute)
	got, err := s.NoAnswer(ctx, "c1", next)
	if err != nil || got.Status != SessionPending || got.Outcome != nip.OutcomeUnknown || !got.NextTSQAt.Equal(next) {
		t.Fatalf("no answer: %+v, %v", got, err)
	}
	if got, err = s.NoAnswer(ctx, "c1", next.Add(time.Hour)); err != nil || !got.NextTSQAt.Equal(next) {
		t.Fatalf("no answer twice: %+v, %v", got, err)
	}
	if _, err := s.NoAnswer(ctx, "nope", next); !errors.Is(err, nip.ErrNoRecord) {
		t.Fatalf("no answer for an unknown session: %v", err)
	}
	register(t, s, "bank-c", "Bank C", "999003", "903")
	if _, err := s.pool.Exec(ctx, `UPDATE institutions SET net_debit_cap_minor = NULL WHERE id = 'bank-c'`); err != nil {
		t.Fatal(err)
	}
	nolimit := transfer("n1", "999002", t0)
	nolimit.FromCode = "999003"
	_, _, err = s.Begin(ctx, nolimit)
	if !errors.Is(err, ErrNoLimits) {
		t.Fatalf("a bank with no cap: %v", err)
	}
}

func TestTSQKeepsTheStoredOutcome(t *testing.T) {
	ctx := context.Background()
	s := sessionStore(t)
	x := transfer("d-b", "999002", t0)
	x.NextTSQAt, x.Outcome = ptr(t0.Add(2*time.Minute)), nip.OutcomeCredited
	if _, err := begin(ctx, s, x); err != nil {
		t.Fatal(err)
	}
	q, err := s.TSQ(ctx, "d-b", t0.Add(10*time.Minute))
	if err != nil || q.Outcome != nip.OutcomeCredited || q.TSQAttempts != 1 || !q.NextTSQAt.Equal(t0.Add(10*time.Minute)) {
		t.Fatalf("tsq keeps stored outcome: %+v, %v", q, err)
	}
}

func TestSettlement(t *testing.T) {
	ctx := context.Background()
	s := sessionStore(t)
	for i, id := range []string{"u1", "u2", "u3", "u4"} {
		if _, err := begin(ctx, s, transfer(id, "999002", t0)); err != nil {
			t.Fatal(err)
		}
		if id == "u4" {
			continue
		}
		if err := s.Resolve(ctx, id, SessionOK, nip.OutcomeCredited, t0.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	un, err := s.Unsettled(ctx, t0.Add(time.Hour))
	if err != nil || len(un) != 2 || un[0].ID != "u1" || un[1].ID != "u2" {
		t.Fatalf("unsettled at cutoff: %+v, %v", un, err)
	}
	if err := s.MarkSettled(ctx, []string{"u2", "u1", "u1"}, "c1", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSettled(ctx, []string{"u1"}, "c1", t0); err != nil {
		t.Fatalf("repeat mark: %v", err)
	}
	wantErr(t, s.MarkSettled(ctx, []string{"u1", "u3"}, "c2", t0), "1 of 2 sessions are already settled in another session")
	wantErr(t, s.MarkSettled(ctx, []string{"u3", "u4"}, "c2", t0), "1 of 2 sessions to settle are not 00")
	err = s.MarkSettled(ctx, []string{"u3", "x1", "x2"}, "c2", t0)
	wantErr(t, err, "2 of 3 sessions to settle do not exist: 25 no record")
	wantErr(t, s.MarkSettled(ctx, nil, "c2", t0), "mark settled needs at least one session id")
	wantErr(t, s.MarkSettled(ctx, []string{"u3"}, "", t0), "mark settled needs a settlement session")
	un, _ = s.Unsettled(ctx, t0.Add(24*time.Hour))
	if len(un) != 1 || un[0].ID != "u3" {
		t.Fatalf("after settling: %+v", un)
	}
	q, _ := s.TSQ(ctx, "u1", t0)
	if q.Settlement != "c1" {
		t.Errorf("u1 settlement %q, want c1", q.Settlement)
	}
}

func TestCallee(t *testing.T) {
	ctx := context.Background()
	s := sessionStore(t)
	good := world.NUBAN("902", 1234567)
	bad := good[:9] + string('0'+(good[9]-'0'+1)%10)
	cases := []struct {
		code, nuban string
		is          error
		want        string
	}{
		{"999002", good, nil, ""},
		{"999002", bad, nip.ErrInvalidAccount, bad + " is not a valid account number at Bank B: 07 invalid account"},
		{"999123", good, nip.ErrUnknownInstitution, "bank code 999123 is not in the NIBSS directory"},
	}
	for _, c := range cases {
		m, err := s.Callee(ctx, c.code, c.nuban)
		if c.is == nil {
			if err != nil || m.ID != "bank-b" {
				t.Errorf("%s/%s: %q, %v", c.code, c.nuban, m.ID, err)
			}
			continue
		}
		if !errors.Is(err, c.is) {
			t.Errorf("%s/%s: %v, want %v", c.code, c.nuban, err, c.is)
		}
		wantErr(t, err, c.want)
	}
	if err := s.SetStatus(ctx, "bank-b", nip.StatusSuspended); err != nil {
		t.Fatal(err)
	}
	m, err := s.Callee(ctx, "999002", good)
	wantErr(t, err, "Bank B is suspended: 91 institution unavailable")
	if m.Name != "Bank B" {
		t.Errorf("suspended bank is named %q, want Bank B", m.Name)
	}
}

func TestResolveKeepsWithheldOutcome(t *testing.T) {
	ctx := context.Background()
	s := sessionStore(t)
	begin := func(id, held string) {
		x := transfer(id, "999002", t0)
		x.Outcome, x.NextTSQAt = held, ptr(t0.Add(time.Minute))
		if _, err := begin(ctx, s, x); err != nil {
			t.Fatal(err)
		}
	}
	at := t0.Add(time.Minute)
	begin("w1", nip.OutcomeCredited)
	wantErr(t, s.Resolve(ctx, "w1", "91", nip.OutcomeFailed, at), "session w1 holds outcome credited and cannot resolve as 91: status change not allowed")
	if err := s.Resolve(ctx, "w1", SessionOK, nip.OutcomeCredited, at); err != nil {
		t.Fatalf("credited as 00: %v", err)
	}
	begin("w2", nip.OutcomeFailed)
	wantErr(t, s.Resolve(ctx, "w2", SessionOK, nip.OutcomeCredited, at), "session w2 holds outcome failed and cannot resolve as 00: status change not allowed")
	if err := s.Resolve(ctx, "w2", "96", nip.OutcomeFailed, at); err != nil {
		t.Fatalf("failed as 96: %v", err)
	}
	begin("w3", nip.OutcomeUnknown)
	if err := s.Resolve(ctx, "w3", SessionOK, nip.OutcomeCredited, at); err != nil {
		t.Fatalf("unknown as 00: %v", err)
	}
	begin("w4", nip.OutcomeUnknown)
	if err := s.Resolve(ctx, "w4", "91", nip.OutcomeFailed, at); err != nil {
		t.Fatalf("unknown as 91: %v", err)
	}
	for id, want := range map[string]string{"w1": SessionOK, "w2": "96", "w3": SessionOK, "w4": "91"} {
		q, err := s.TSQ(ctx, id, at)
		if err != nil || q.Status != want {
			t.Errorf("%s: %+v, %v; want status %s", id, q, err, want)
		}
	}
}
