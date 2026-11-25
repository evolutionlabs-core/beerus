package nibss

import (
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

func settledBooks(t *testing.T, s *Store) {
	t.Helper()
	pay(t, s, "p1", "999001", "999002", 100, "", -1)
	pay(t, s, "p2", "999002", "999001", 20, "", -1)
	pay(t, s, "r1", "999001", "999002", 500, SessionOK, 0)
	pay(t, s, "r2", "999002", "999001", 200, SessionOK, 0)
	pay(t, s, "r3", "999001", "999002", 70, SessionOK, 3*time.Hour)
	pay(t, s, "f1", "999001", "999002", 9, "61", 0)
	pay(t, s, "r4", "999001", "999002", 11, SessionOK, 11*time.Hour)
	_, err := s.Settle(t.Context(), Schedule{Per: 4}, t0.Add(11*time.Hour), false)
	must(t, err)
}

const noon, evening = "2026-11-04T12:00", "2026-11-04T18:00"

func lagos(h int) time.Time { return time.Date(2026, 11, 4, h, 0, 0, 0, clock.Lagos) }

func msg(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}

func settlementPosition(t *testing.T, s *Store) {
	ctx := t.Context()
	settledBooks(t, s)
	check := func(name, identity, after string, want SettlementPosition, wantErr string) {
		t.Helper()
		got, err := s.SettlementPosition(ctx, identity, after)
		if got != want || msg(err) != wantErr {
			t.Errorf("%s: %+v %q, want %+v %q", name, got, msg(err), want, wantErr)
		}
	}
	check("bank a with nothing posted", "bank-a", "", SettlementPosition{681, 200, 20, 6, 2}, "")
	check("bank c is in nothing", "bank-c", "", SettlementPosition{}, "")
	check("a stranger", "bank-z", "", SettlementPosition{}, "settlement position for bank-z: not in the NIBSS directory")
	must(t, s.MarkPosted(ctx, noon, lagos(12)))
	must(t, s.MarkPosted(ctx, evening, lagos(18)))
	check("bank a after the noon session", "bank-a", noon, SettlementPosition{181, 0, 20, 4, 1}, "")
	check("bank a after a session NIBSS does not know", "bank-a", "2020-01-01T00:00", SettlementPosition{681, 200, 20, 6, 2}, "")
}

func openSessions(t *testing.T, s *Store) {
	settledBooks(t, s)
	for pass, unposted := range []int{2, 1} {
		for _, r := range []struct {
			id   string
			want OpenSessions
			err  string
		}{
			{"bank-a", OpenSessions{BankID: "bank-a", Transfers: 3, Outward: 111, PendingInward: 20, Unposted: unposted}, ""},
			{"bank-c", OpenSessions{BankID: "bank-c"}, ""},
			{"bank-z", OpenSessions{}, "bank id bank-z is not in the NIBSS directory"},
		} {
			got, err := s.OpenSessions(t.Context(), r.id)
			if got != r.want || msg(err) != r.err {
				t.Errorf("pass %d, %s: %+v %q, want %+v %q", pass, r.id, got, msg(err), r.want, r.err)
			}
		}
		must(t, s.MarkPosted(t.Context(), noon, t0))
	}
}

func sumsBeyondSixtyFourBits(t *testing.T, s *Store) {
	ctx := t.Context()
	for _, id := range []string{"big1", "big2"} {
		_, err := s.pool.Exec(ctx, `INSERT INTO nip_sessions (session_id, from_code, to_code, from_nuban, to_nuban, amount_minor, narration, status, outcome, created_at, resolved_at)
			VALUES ($1, '999001', '999002', '0000000011', '0000000022', $2, 'rent', '00', 'credited', $3, $3)`, id, int64(math.MaxInt64), t0)
		must(t, err)
	}
	sum := func(name string, err error, want string) {
		t.Helper()
		var sumErr *SumError
		if msg(err) != want || !errors.As(err, &sumErr) {
			t.Errorf("%s: %q, want %q", name, msg(err), want)
		}
	}
	_, err := s.SettlementPosition(ctx, "bank-a", "")
	sum("position of the sender", err, "the open sessions of 999001 add up to more than 64 bits")
	_, err = s.SettlementPosition(ctx, "bank-b", "")
	sum("position of the receiver", err, "the open sessions of 999002 add up to more than 64 bits")
	_, err = s.OpenSessions(ctx, "bank-a")
	sum("open sessions of the sender", err, "the open sessions of bank-a add up to more than 64 bits")
	_, err = s.Settle(ctx, Schedule{Per: 4}, t0.Add(11*time.Hour), false)
	must(t, err)
	must(t, s.MarkPosted(ctx, noon, lagos(12)))
	_, _, err = s.SettlementReports(ctx, "bank-a", "", 10, "")
	sum("reports of the sender", err, "settle 2026-11-04T12:00 at 999001: the amount sent is too large to add")
	_, _, err = s.SettlementReports(ctx, "bank-b", "", 10, "")
	sum("reports of the receiver", err, "settle 2026-11-04T12:00 at 999002: the amount received is too large to add")
}

func settlementReports(t *testing.T, s *Store) {
	ctx := t.Context()
	pay(t, s, "r1", "999001", "999002", 500, SessionOK, 0)
	pay(t, s, "r2", "999002", "999001", 200, SessionOK, 0)
	pay(t, s, "r3", "999001", "999002", 70, SessionOK, 3*time.Hour)
	pay(t, s, "r4", "999002", "999003", 30, SessionOK, 8*time.Hour)
	_, err := s.Settle(ctx, Schedule{Per: 4}, t0.Add(11*time.Hour), false)
	must(t, err)
	report := func(key string, h int, outward, inward, net int64, transfers int) Report {
		return Report{SessionID: key, CutoffAt: lagos(h), PostedAt: lagos(h), Outward: outward, Inward: inward, Net: net, Transfers: transfers}
	}
	list := func(identity, after string, limit int, cursor string) ([]Report, string, string) {
		got, next, err := s.SettlementReports(ctx, identity, after, limit, cursor)
		return got, next, msg(err)
	}
	if got, next, m := list("bank-a", "", 10, ""); len(got) != 0 || next != "" || m != "" {
		t.Fatalf("before posting: %+v %q %q", got, next, m)
	}
	must(t, s.MarkPosted(ctx, evening, lagos(18)))
	must(t, s.MarkPosted(ctx, noon, lagos(12)))
	first, second := report(noon, 12, 500, 200, -300, 2), report(evening, 18, 70, 0, -70, 1)
	for _, r := range []struct {
		name, identity, after string
		limit                 int
		cursor                string
		want                  []Report
		more                  bool
		err                   string
	}{
		{"bank a reads both", "bank-a", "", 10, "", []Report{first, second}, false, ""},
		{"after the first", "bank-a", noon, 10, "", []Report{second}, false, ""},
		{"after the last", "bank-a", evening, 10, "", []Report{}, false, ""},
		{"a stranger", "bank-z", "", 10, "", nil, false, "settlement reports for bank-z: not in the NIBSS directory"},
		{"a cursor that is not ours", "bank-a", "", 10, "eA", nil, false, `cursor "eA" is not valid; use the next value from the last page`},
		{"a limit that is too large", "bank-a", "", 5000, "", nil, false, "limit must be from 1 to 1000, not 5000"},
	} {
		got, next, m := list(r.identity, r.after, r.limit, r.cursor)
		if !reflect.DeepEqual(got, r.want) || (next != "") != r.more || m != r.err {
			t.Errorf("%s: %+v %q %q, want %+v more %v %q", r.name, got, next, m, r.want, r.more, r.err)
		}
	}
	pages(t, "reports", func(limit int, cursor string) ([]string, string, error) {
		got, next, err := s.SettlementReports(ctx, "bank-a", "", limit, cursor)
		ids := []string{}
		for _, r := range got {
			ids = append(ids, r.SessionID)
		}
		return ids, next, err
	}, noon, evening)
}
