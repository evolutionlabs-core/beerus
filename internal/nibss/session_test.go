package nibss

import (
	"context"
	"errors"
	"math"
	"strings"
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
	for _, m := range []Member{member("bank-a", "Bank A", "999001", "901"), member("bank-b", "Bank B", "999002", "902"), member("bank-c", "Bank C", "999003", "903")} {
		m.Limits = Limits{NetDebitCap: 100_000_000}
		m.CallerID = m.ID
		if err := s.Register(context.Background(), m); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func resetSessions(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(), `TRUNCATE nip_sessions, settlement_sessions CASCADE`); err != nil {
		t.Fatal(err)
	}
}

func begin(ctx context.Context, s *Store, x Session) (Session, error) {
	got, _, err := s.Begin(ctx, x)
	return got, err
}

func transfer(id, to string, at time.Time) Session {
	return Session{ID: id, FromCode: "999001", ToCode: to, FromNUBAN: world.NUBAN("901", 1), ToNUBAN: world.NUBAN("902", 2), Amount: 500000, Narration: "rent", CreatedAt: at}
}

func ptr(t time.Time) *time.Time { return &t }

func pay(t *testing.T, s *Store, id, from, to string, amount int64, status string, resolved time.Duration) {
	t.Helper()
	ctx := context.Background()
	x := transfer(id, to, t0)
	x.FromCode, x.Amount = from, amount
	if _, err := begin(ctx, s, x); err != nil {
		t.Fatal(err)
	}
	if resolved < 0 {
		return
	}
	outcome := nip.OutcomeCredited
	if status != SessionOK {
		outcome = nip.OutcomeFailed
	}
	if err := s.Resolve(ctx, id, status, outcome, t0.Add(resolved)); err != nil {
		t.Fatal(err)
	}
}

func TestSessions(t *testing.T) {
	s := sessionStore(t)
	for _, c := range []struct {
		name string
		run  func(*testing.T, *Store)
	}{
		{"lifecycle", sessionLifecycle},
		{"bad input", sessionBadInput},
		{"settlement marks", sessionSettlement},
		{"callee", sessionCallee},
		{"settle by cutoff", settleByCutoff},
		{"settle on the calendar", settleOnTheCalendar},
		{"settle on demand", settleOnDemand},
		{"problems", settlementProblems},
		{"position", settlementPosition},
		{"open sessions", openSessions},
		{"sums beyond 64 bits", sumsBeyondSixtyFourBits},
		{"reports", settlementReports},
	} {
		resetSessions(t, s)
		t.Run(c.name, func(t *testing.T) { c.run(t, s) })
	}
}

func sessionLifecycle(t *testing.T, s *Store) {
	ctx := context.Background()
	x := transfer("s1", "999002", t0)
	x.NextTSQAt = ptr(t0.Add(2 * time.Minute))
	got, err := begin(ctx, s, x)
	if err != nil || got.Status != SessionPending || got.Outcome != "" || got.ResolvedAt != nil || !got.CreatedAt.Equal(t0) || !got.NextTSQAt.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("begin: %+v, %v", got, err)
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
	wantErr(t, s.Resolve(ctx, "s1", "91", nip.OutcomeFailed, t0.Add(time.Hour)), "session s1 is 00/credited and cannot become 91/failed: status change not allowed")
	q, err = s.TSQ(ctx, "s1", t0.Add(8*time.Minute))
	if err != nil || q.TSQAttempts != 1 || q.Status != SessionOK || q.Outcome != nip.OutcomeCredited || !q.ResolvedAt.Equal(t0.Add(time.Minute)) || q.NextTSQAt != nil {
		t.Fatalf("tsq after resolve: %+v, %v", q, err)
	}

	pay(t, s, "s2", "999001", "999002", 5, "91", 0)
	if err := s.Resolve(ctx, "s2", "91", nip.OutcomeFailed, t0); err != nil {
		t.Fatalf("repeat failure: %v", err)
	}
	wantErr(t, s.Resolve(ctx, "s2", SessionOK, nip.OutcomeCredited, t0), "session s2 is 91/failed and cannot become 00/credited: status change not allowed")

	kept := transfer("d-b", "999002", t0)
	kept.NextTSQAt, kept.Outcome = ptr(t0.Add(2*time.Minute)), nip.OutcomeCredited
	if _, err := begin(ctx, s, kept); err != nil {
		t.Fatal(err)
	}
	if q, err := s.TSQ(ctx, "d-b", t0.Add(10*time.Minute)); err != nil || q.Outcome != nip.OutcomeCredited || q.TSQAttempts != 1 || !q.NextTSQAt.Equal(t0.Add(10*time.Minute)) {
		t.Fatalf("tsq keeps stored outcome: %+v, %v", q, err)
	}

	at := t0.Add(time.Minute)
	for _, w := range []struct{ id, held, status, outcome, err string }{
		{"w1", nip.OutcomeCredited, "91", nip.OutcomeFailed, "session w1 holds outcome credited and cannot resolve as 91: status change not allowed"},
		{"w1", nip.OutcomeCredited, SessionOK, nip.OutcomeCredited, ""},
		{"w2", nip.OutcomeFailed, SessionOK, nip.OutcomeCredited, "session w2 holds outcome failed and cannot resolve as 00: status change not allowed"},
		{"w2", nip.OutcomeFailed, "96", nip.OutcomeFailed, ""},
		{"w3", nip.OutcomeUnknown, SessionOK, nip.OutcomeCredited, ""},
		{"w4", nip.OutcomeUnknown, "91", nip.OutcomeFailed, ""},
	} {
		x := transfer(w.id, "999002", t0)
		x.Outcome, x.NextTSQAt = w.held, ptr(at)
		if _, err := begin(ctx, s, x); err != nil {
			t.Fatal(err)
		}
		err := s.Resolve(ctx, w.id, w.status, w.outcome, at)
		if w.err == "" && err != nil || w.err != "" && (err == nil || err.Error() != w.err) {
			t.Errorf("%s held %s resolved as %s: %v, want %q", w.id, w.held, w.status, err, w.err)
		}
	}
	for id, want := range map[string]string{"w1": SessionOK, "w2": "96", "w3": SessionOK, "w4": "91"} {
		if q, err := s.TSQ(ctx, id, at); err != nil || q.Status != want {
			t.Errorf("%s: %+v, %v; want status %s", id, q, err, want)
		}
	}
}

func sessionBadInput(t *testing.T, s *Store) {
	ctx := context.Background()
	if _, err := begin(ctx, s, transfer("s3", "999002", t0)); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
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
	} {
		wantErr(t, s.Resolve(ctx, "s3", c.status, c.outcome, c.at), c.want)
	}
	if err := s.Resolve(ctx, "nope", SessionOK, nip.OutcomeCredited, t0); !errors.Is(err, nip.ErrNoRecord) {
		t.Fatalf("got %v, want nip.ErrNoRecord", err)
	}
	_, err := s.TSQ(ctx, "s3", time.Time{})
	wantErr(t, err, "session s3 needs the next status query time")
	if _, err = s.TSQ(ctx, "nope", t0); !errors.Is(err, nip.ErrNoRecord) {
		t.Fatalf("got %v, want nip.ErrNoRecord", err)
	}
	mod := func(f func(*Session)) Session {
		x := transfer("s4", "999002", t0)
		f(&x)
		return x
	}
	for _, c := range []struct {
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
		{mod(func(x *Session) { x.FromCode = "999009" }), "bank code 999009 is not in the NIBSS directory"},
	} {
		_, err := begin(ctx, s, c.x)
		wantErr(t, err, c.want)
	}
}

func sessionSettlement(t *testing.T, s *Store) {
	ctx := context.Background()
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
	wantErr(t, s.MarkSettled(ctx, []string{"u3", "x1", "x2"}, "c2", t0), "2 of 3 sessions to settle do not exist: 25 no record")
	wantErr(t, s.MarkSettled(ctx, nil, "c2", t0), "mark settled needs at least one session id")
	wantErr(t, s.MarkSettled(ctx, []string{"u3"}, "", t0), "mark settled needs a settlement session")
	if un, _ = s.Unsettled(ctx, t0.Add(24*time.Hour)); len(un) != 1 || un[0].ID != "u3" {
		t.Fatalf("after settling: %+v", un)
	}
	if q, _ := s.TSQ(ctx, "u1", t0); q.Settlement != "c1" {
		t.Errorf("u1 settlement %q, want c1", q.Settlement)
	}
}

func sessionCallee(t *testing.T, s *Store) {
	ctx := context.Background()
	good := world.NUBAN("902", 1234567)
	bad := good[:9] + string('0'+(good[9]-'0'+1)%10)
	for _, c := range []struct {
		code, nuban string
		is          error
		want        string
	}{
		{"999002", good, nil, ""},
		{"999002", bad, nip.ErrInvalidAccount, bad + " is not a valid account number at Bank B: 07 invalid account"},
		{"999123", good, nip.ErrUnknownInstitution, "bank code 999123 is not in the NIBSS directory"},
	} {
		m, err := s.Callee(ctx, c.code, c.nuban)
		if c.is == nil && (err != nil || m.ID != "bank-b") || c.is != nil && (!errors.Is(err, c.is) || err.Error() != c.want) {
			t.Errorf("%s/%s: %q, %v, want %v %q", c.code, c.nuban, m.ID, err, c.is, c.want)
		}
	}
}

func TestSessionGatesAndLimits(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	registerCapped(t, s, "bank-a", "Bank A", "999001", "901", Limits{NetDebitCap: 1_200_000, Collateral: 5})
	registerCapped(t, s, "bank-b", "Bank B", "999002", "902", Limits{NetDebitCap: 100_000_000})
	for _, r := range []struct {
		id       string
		amount   int64
		status   string
		replayed bool
	}{
		{"c1", 500_000, SessionPending, false},
		{"c2", 700_000, SessionPending, false},
		{"c3", 1, "61", false},
		{"c3", 1, "61", true},
		{"c4", 5_000_000, "61", false},
		{"c5", math.MaxInt64, "61", false},
	} {
		x := transfer(r.id, "999002", t0)
		x.Amount = r.amount
		got, replayed, err := s.Begin(ctx, x)
		if err != nil || got.Status != r.status || replayed != r.replayed {
			t.Fatalf("%s: %+v replayed %v, %v", r.id, got, replayed, err)
		}
	}
	if pos, err := position(ctx, s.pool, "999001"); err != nil || pos.Outward != 1_200_000 || pos.Inward != 0 {
		t.Fatalf("position of the sender: %+v, %v", pos, err)
	}
	if pos, err := position(ctx, s.pool, "999002"); err != nil || pos.Inward != 1_200_000 || pos.NetDebit() != 0 {
		t.Fatalf("position of the receiver: %+v, %v", pos, err)
	}
	changed := transfer("c1", "999002", t0)
	changed.Amount, changed.Narration = 1, "other"
	_, _, err := s.Begin(ctx, changed)
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
	if _, _, err = s.Begin(ctx, nolimit); !errors.Is(err, ErrNoLimits) {
		t.Fatalf("a bank with no cap: %v", err)
	}

	resetSessions(t, s)
	setStatus(t, s, "bank-b", nip.StatusSuspended)
	m, err := s.Callee(ctx, "999002", world.NUBAN("902", 1234567))
	wantErr(t, err, "Bank B is suspended: 91 institution unavailable")
	if m.Name != "Bank B" {
		t.Errorf("suspended bank is named %q, want Bank B", m.Name)
	}
	for _, r := range []struct {
		name    string
		prepare func()
		from    string
		closure bool
		status  string
		outcome string
		err     string
	}{
		{name: "to suspended", from: "999001", status: "91", outcome: "failed"},
		{name: "both active", prepare: func() { setStatus(t, s, "bank-b", nip.StatusActive) }, from: "999001", status: SessionPending},
		{name: "closure from active", from: "999001", closure: true, err: "Bank A is active, so it cannot send a closure transfer"},
		{name: "from terminating", prepare: func() { setStatus(t, s, "bank-a", nip.StatusTerminating) }, from: "999001", status: "91", outcome: "failed"},
		{name: "closure from terminating", from: "999001", closure: true, status: SessionPending},
		{name: "closure to terminating", prepare: func() { setStatus(t, s, "bank-b", nip.StatusTerminating) }, from: "999001", closure: true, status: "91", outcome: "failed"},
		{name: "from removed", prepare: func() { setStatus(t, s, "bank-a", nip.StatusRemoved) }, from: "999001", closure: true, err: "Bank A is removed, so it cannot send a closure transfer"},
	} {
		if r.prepare != nil {
			r.prepare()
		}
		x := transfer("g-"+strings.ReplaceAll(r.name, " ", "-"), "999002", t0)
		x.FromCode, x.Closure = r.from, r.closure
		got, err := begin(ctx, s, x)
		if r.err != "" {
			var closure *ClosureError
			if !errors.As(err, &closure) || err.Error() != r.err {
				t.Errorf("%s: got %v, want %s", r.name, err, r.err)
			}
			continue
		}
		stored, serr := s.Session(ctx, x.ID)
		if err != nil || got.Status != r.status || got.Outcome != r.outcome || (r.status != SessionPending) != (got.ResolvedAt != nil) || serr != nil || stored.Status != r.status || stored.Outcome != r.outcome {
			t.Errorf("%s: %+v, %v; stored %+v, %v", r.name, got, err, stored, serr)
		}
	}
}

func TestClosureSessionsListOnePageAtATime(t *testing.T) {
	ctx := t.Context()
	s := sessionStore(t)
	setStatus(t, s, "bank-a", nip.StatusTerminating)
	closure := func(id, from string, account uint64, at time.Time) Session {
		x := transfer(id, "999002", at)
		x.FromCode, x.FromNUBAN, x.Closure = from, world.NUBAN("901", account), true
		return x
	}
	for _, x := range []Session{closure("c2", "999001", 12, t0), closure("c1", "999001", 11, t0), closure("c3", "999001", 13, t0.Add(time.Minute))} {
		if _, err := begin(ctx, s, x); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Resolve(ctx, "c1", SessionOK, nip.OutcomeCredited, t0); err != nil {
		t.Fatal(err)
	}
	setStatus(t, s, "bank-b", nip.StatusTerminating)
	for _, x := range []Session{closure("c4", "999001", 13, t0.Add(2*time.Minute)), closure("x1", "999002", 21, t0)} {
		if got, err := begin(ctx, s, x); err != nil || got.Status != "91" {
			t.Fatalf("failed closure: %+v, %v", got, err)
		}
	}
	if _, err := begin(ctx, s, transfer("n1", "999002", t0)); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct {
		name  string
		code  string
		limit int
		after string
		want  string
		next  string
		err   string
	}{
		{"every session in order", "999001", 10, "", "c1 00|c2 pending|c3 pending|c4 91", "", ""},
		{"first page", "999001", 2, "", "c1 00|c2 pending", "c2", ""},
		{"second page", "999001", 2, "c2", "c3 pending|c4 91", "", ""},
		{"page that ends exactly", "999001", 4, "", "c1 00|c2 pending|c3 pending|c4 91", "", ""},
		{"after the last", "999001", 2, "c4", "", "", ""},
		{"the other bank", "999002", 10, "", "x1 91", "", ""},
		{"a bank without closures", "999003", 10, "", "", "", ""},
		{"a cursor of another bank", "999001", 2, "x1", "", "", `cursor "x1" is not valid; use the next value from the last page`},
		{"an unknown cursor", "999001", 2, "nope", "", "", `cursor "nope" is not valid; use the next value from the last page`},
	} {
		got, next, err := s.ClosureSessions(ctx, r.code, r.limit, r.after)
		parts := []string{}
		for _, x := range got {
			parts = append(parts, x.ID+" "+x.Status)
		}
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		if have := strings.Join(parts, "|"); have != r.want || next != r.next || msg != r.err {
			t.Errorf("%s: %q, next %q, error %q; want %q, next %q, error %q", r.name, have, next, msg, r.want, r.next, r.err)
		}
	}
	got, _, err := s.ClosureSessions(ctx, "999001", 1, "")
	want := ClosureSession{ID: "c1", FromAccount: world.NUBAN("901", 11), ToBankCode: "999002", ToAccount: world.NUBAN("902", 2), Amount: 500000, Status: SessionOK, CreatedAt: t0}
	if err != nil || len(got) != 1 || got[0].ID != want.ID || got[0].FromAccount != want.FromAccount || got[0].ToBankCode != want.ToBankCode || got[0].ToAccount != want.ToAccount || got[0].Amount != want.Amount || got[0].Status != want.Status || !got[0].CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("first closure session: %+v, %v; want %+v", got, err, want)
	}
}
