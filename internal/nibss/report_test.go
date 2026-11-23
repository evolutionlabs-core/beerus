package nibss

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/nip"
)

func TestSettlementReportsListOnlyPostedSessionsOfTheCaller(t *testing.T) {
	ctx := t.Context()
	s := testStore(t)
	for _, m := range []Member{member("bank-a", "Bank A", "999001", "901"), member("bank-b", "Bank B", "999002", "902"), member("bank-c", "Bank C", "999003", "903")} {
		m.Limits = Limits{NetDebitCap: 100_000_000}
		m.CallerID = m.ID
		if err := s.Register(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	pay := func(id, from, to string, amount int64, resolved time.Duration) {
		x := transfer(id, to, t0)
		x.FromCode, x.Amount = from, amount
		if _, err := begin(ctx, s, x); err != nil {
			t.Fatal(err)
		}
		if err := s.Resolve(ctx, id, SessionOK, nip.OutcomeCredited, t0.Add(resolved)); err != nil {
			t.Fatal(err)
		}
	}
	pay("r1", "999001", "999002", 500, 0)
	pay("r2", "999002", "999001", 200, 0)
	pay("r3", "999001", "999002", 70, 3*time.Hour)
	pay("r4", "999002", "999003", 30, 8*time.Hour)
	if _, err := s.Settle(ctx, Schedule{Per: 4}, t0.Add(11*time.Hour), false); err != nil {
		t.Fatal(err)
	}
	const noon, evening = "2026-11-04T12:00", "2026-11-04T18:00"
	at := func(h int) time.Time { return time.Date(2026, 11, 4, h, 0, 0, 0, clock.Lagos) }
	report := func(key string, h int, outward, inward, net int64, transfers int) Report {
		return Report{SessionID: key, CutoffAt: at(h), PostedAt: at(h), Outward: outward, Inward: inward, Net: net, Transfers: transfers}
	}
	list := func(identity, after string, limit int, cursor string) ([]Report, string, string) {
		got, next, err := s.SettlementReports(ctx, identity, after, limit, cursor)
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		return got, next, msg
	}
	if got, next, msg := list("bank-a", "", 10, ""); len(got) != 0 || next != "" || msg != "" {
		t.Fatalf("before posting: %+v %q %q", got, next, msg)
	}
	if err := s.MarkPosted(ctx, evening, at(18)); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkPosted(ctx, noon, at(12)); err != nil {
		t.Fatal(err)
	}
	first, second := report(noon, 12, 500, 200, -300, 2), report(evening, 18, 70, 0, -70, 1)
	rows := []struct {
		name     string
		identity string
		after    string
		limit    int
		cursor   string
		want     []Report
		more     bool
		err      string
	}{
		{"bank a reads both", "bank-a", "", 10, "", []Report{first, second}, false, ""},
		{"bank b reads its own", "bank-b", "", 10, "", []Report{report(noon, 12, 200, 500, 300, 2), report(evening, 18, 30, 70, 40, 2)}, false, ""},
		{"bank c is in one session", "bank-c", "", 10, "", []Report{report(evening, 18, 0, 30, 30, 1)}, false, ""},
		{"first page", "bank-a", "", 1, "", []Report{first}, true, ""},
		{"after the first", "bank-a", noon, 10, "", []Report{second}, false, ""},
		{"after the last", "bank-a", evening, 10, "", []Report{}, false, ""},
		{"after a session NIBSS does not know", "bank-a", "2020-01-01T00:00", 10, "", []Report{first, second}, false, ""},
		{"a bad cursor", "bank-a", "", 10, "not-a-cursor", nil, false, `cursor "not-a-cursor" is not valid; use the next value from the last page`},
		{"a stranger", "bank-z", "", 10, "", nil, false, "settlement reports for bank-z: not in the NIBSS directory"},
		{"a limit that is too large", "bank-a", "", 5000, "", nil, false, "limit must be from 1 to 1000, not 5000"},
	}
	for _, r := range rows {
		got, next, msg := list(r.identity, r.after, r.limit, r.cursor)
		if !reflect.DeepEqual(got, r.want) || (next != "") != r.more || msg != r.err {
			t.Errorf("%s: %+v %q %q, want %+v more %v %q", r.name, got, next, msg, r.want, r.more, r.err)
		}
	}
	_, next, msg := list("bank-a", "", 1, "")
	if msg != "" {
		t.Fatal(msg)
	}
	if got, last, msg := list("bank-a", "", 1, next); !reflect.DeepEqual(got, []Report{second}) || last != "" || msg != "" {
		t.Errorf("second page: %+v %q %q", got, last, msg)
	}
	var cursor *CursorError
	if _, _, err := s.SettlementReports(ctx, "bank-a", "", 10, "x"); !errors.As(err, &cursor) {
		t.Errorf("a bad cursor is %T", err)
	}
}
