package nibss

import (
	"errors"
	"math"
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
	if _, err := s.Settle(t.Context(), Schedule{Per: 4}, t0.Add(11*time.Hour), false); err != nil {
		t.Fatal(err)
	}
}

func settlementPosition(t *testing.T, s *Store) {
	ctx := t.Context()
	settledBooks(t, s)
	const noon, evening = "2026-11-04T12:00", "2026-11-04T18:00"
	at := func(h int) time.Time { return time.Date(2026, 11, 4, h, 0, 0, 0, clock.Lagos) }
	read := func(identity, after string) (SettlementPosition, string) {
		got, err := s.SettlementPosition(ctx, identity, after)
		if err != nil {
			return got, err.Error()
		}
		return got, ""
	}
	type row struct {
		name     string
		identity string
		after    string
		want     SettlementPosition
		err      string
	}
	check := func(rows []row) {
		t.Helper()
		for _, r := range rows {
			if got, msg := read(r.identity, r.after); got != r.want || msg != r.err {
				t.Errorf("%s: %+v %q, want %+v %q", r.name, got, msg, r.want, r.err)
			}
		}
	}
	check([]row{
		{"bank a with nothing posted", "bank-a", "", SettlementPosition{681, 200, 20, 6, 2}, ""},
		{"bank b with nothing posted", "bank-b", "", SettlementPosition{220, 581, 100, 6, 2}, ""},
		{"bank c is in nothing", "bank-c", "", SettlementPosition{}, ""},
		{"a stranger", "bank-z", "", SettlementPosition{}, "settlement position for bank-z: not in the NIBSS directory"},
	})
	for key, h := range map[string]int{noon: 12, evening: 18} {
		if err := s.MarkPosted(ctx, key, at(h)); err != nil {
			t.Fatal(err)
		}
	}
	check([]row{
		{"bank a after nothing", "bank-a", "", SettlementPosition{681, 200, 20, 6, 2}, ""},
		{"bank a after the noon session", "bank-a", noon, SettlementPosition{181, 0, 20, 4, 1}, ""},
		{"bank a after the evening session", "bank-a", evening, SettlementPosition{111, 0, 20, 3, 0}, ""},
		{"bank a after a session NIBSS does not know", "bank-a", "2020-01-01T00:00", SettlementPosition{681, 200, 20, 6, 2}, ""},
		{"bank b after the noon session", "bank-b", noon, SettlementPosition{20, 81, 100, 4, 1}, ""},
		{"bank c after nothing", "bank-c", "", SettlementPosition{}, ""},
	})
}

func openSessions(t *testing.T, s *Store) {
	ctx := t.Context()
	settledBooks(t, s)
	rows := []struct {
		name string
		id   string
		want OpenSessions
		err  string
	}{
		{"bank a", "bank-a", OpenSessions{BankID: "bank-a", Transfers: 3, Outward: 111, Inward: 0, PendingInward: 20, Unposted: 2}, ""},
		{"bank b", "bank-b", OpenSessions{BankID: "bank-b", Transfers: 3, Outward: 20, Inward: 11, PendingInward: 100, Unposted: 2}, ""},
		{"bank c", "bank-c", OpenSessions{BankID: "bank-c"}, ""},
		{"an unknown bank", "bank-z", OpenSessions{}, "bank id bank-z is not in the NIBSS directory"},
	}
	for pass := 0; pass < 2; pass++ {
		for _, r := range rows {
			got, err := s.OpenSessions(ctx, r.id)
			msg := ""
			if err != nil {
				msg = err.Error()
			}
			if got != r.want || msg != r.err {
				t.Errorf("pass %d, %s: %+v %q, want %+v %q", pass, r.name, got, msg, r.want, r.err)
			}
		}
		if err := s.MarkPosted(ctx, "2026-11-04T12:00", t0); err != nil {
			t.Fatal(err)
		}
		rows[0].want.Unposted, rows[1].want.Unposted = 1, 1
	}
}

func sumsBeyondSixtyFourBits(t *testing.T, s *Store) {
	ctx := t.Context()
	for _, id := range []string{"big1", "big2"} {
		if _, err := s.pool.Exec(ctx, `INSERT INTO nip_sessions (session_id, from_code, to_code, from_nuban, to_nuban, amount_minor, narration, status, outcome, created_at, resolved_at)
			VALUES ($1, '999001', '999002', '0000000011', '0000000022', $2, 'rent', '00', 'credited', $3, $3)`, id, int64(math.MaxInt64), t0); err != nil {
			t.Fatal(err)
		}
	}
	type row struct {
		name string
		run  func() error
		want string
	}
	check := func(rows []row) {
		t.Helper()
		for _, r := range rows {
			err := r.run()
			var sum *SumError
			got := ""
			if err != nil {
				got = err.Error()
			}
			if got != r.want || (r.want != "" && !errors.As(err, &sum)) {
				t.Errorf("%s: %q, want %q", r.name, got, r.want)
			}
		}
	}
	check([]row{
		{"position of the sender", func() error { _, err := s.SettlementPosition(ctx, "bank-a", ""); return err }, "the open sessions of 999001 add up to more than 64 bits"},
		{"position of the receiver", func() error { _, err := s.SettlementPosition(ctx, "bank-b", ""); return err }, "the open sessions of 999002 add up to more than 64 bits"},
		{"open sessions of the sender", func() error { _, err := s.OpenSessions(ctx, "bank-a"); return err }, "the open sessions of bank-a add up to more than 64 bits"},
		{"open sessions of the receiver", func() error { _, err := s.OpenSessions(ctx, "bank-b"); return err }, "the open sessions of bank-b add up to more than 64 bits"},
	})
	if _, err := s.Settle(ctx, Schedule{Per: 4}, t0.Add(11*time.Hour), false); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkPosted(ctx, "2026-11-04T12:00", time.Date(2026, 11, 4, 12, 0, 0, 0, clock.Lagos)); err != nil {
		t.Fatal(err)
	}
	check([]row{
		{"reports of the sender", func() error { _, _, err := s.SettlementReports(ctx, "bank-a", "", 10, ""); return err }, "settle 2026-11-04T12:00 at 999001: the amount sent is too large to add"},
		{"reports of the receiver", func() error { _, _, err := s.SettlementReports(ctx, "bank-b", "", 10, ""); return err }, "settle 2026-11-04T12:00 at 999002: the amount received is too large to add"},
	})
}
