package nibss

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/nip"
)

func settleKeys(t *testing.T, s *Store, sched Schedule, now time.Time) []string {
	t.Helper()
	got, err := s.Settle(t.Context(), sched, now, false)
	must(t, err)
	keys := []string{}
	for _, x := range got {
		keys = append(keys, x.Key)
	}
	return keys
}

func settleByCutoff(t *testing.T, s *Store) {
	ctx := t.Context()
	sched := Schedule{Per: 4}
	for i, id := range []string{"v1", "v2", "v3", "v4"} {
		pay(t, s, id, "999001", "999002", 500000, SessionOK, time.Duration(i*i)*time.Hour)
	}
	both := []string{"2026-11-04T12:00", "2026-11-04T18:00"}
	if got := settleKeys(t, s, sched, t0.Add(2*time.Hour)); len(got) != 0 {
		t.Fatalf("before the first cutoff: %v", got)
	}
	settled, err := s.Settle(ctx, sched, t0.Add(11*time.Hour), false)
	nets, nerr := settled[0].Nets(map[string]string{"999001": "bank-a", "999002": "bank-b"})
	want := []BankNet{{"bank-a", -1000000}, {"bank-b", 1000000}}
	if err != nil || nerr != nil || len(settled) != 2 || settled[0].Key != both[0] || !reflect.DeepEqual(nets, want) || len(Moved(nets)) != 2 {
		t.Fatalf("the passed cutoffs: %+v %v, nets %+v %v", settled, err, nets, nerr)
	}
	if got := settleKeys(t, s, sched, t0.Add(11*time.Hour)); !reflect.DeepEqual(got, both) {
		t.Fatalf("the same pass again: %v", got)
	}
	if _, err := (Settlement{Key: "k", Sessions: []Session{transfer("x", "999002", t0)}}).Nets(map[string]string{"999001": "bank-a"}); err == nil || !strings.Contains(err.Error(), "session x is between 999001 and 999002, and one of them is not in the directory") {
		t.Fatalf("nets with a bank that is not in the directory: %v", err)
	}
	if got := Moved([]BankNet{{"bank-a", -5}, {"bank-b", 0}, {"bank-c", 5}}); !reflect.DeepEqual(got, []BankNet{{"bank-a", -5}, {"bank-c", 5}}) {
		t.Fatalf("moved: %+v", got)
	}
	for _, key := range both {
		must(t, s.MarkPosted(ctx, key, t0))
	}
	if n, err := s.PostedCount(ctx); err != nil || n != 2 {
		t.Fatalf("posted %d, %v", n, err)
	}
	if got := settleKeys(t, s, sched, t0.Add(11*time.Hour)); len(got) != 0 {
		t.Fatalf("after posting: %v", got)
	}
	must(t, s.KeepSchedule(ctx, 4))
	must(t, s.KeepSchedule(ctx, 4))
	wantErr(t, s.KeepSchedule(ctx, 12), "nip_sessions changed: NIBSS has settled with 4 sessions a day, so nip_sessions must stay 4")
}

func settleOnTheCalendar(t *testing.T, s *Store) {
	ctx := t.Context()
	monday := clock.Date{Y: 2026, M: time.November, D: 9}
	plain, closed := Schedule{Per: 4}, Schedule{Per: 4, Cal: clock.NewCalendar(nil, []clock.Date{monday}, nil)}
	for _, c := range []struct {
		name     string
		sched    Schedule
		resolved string
		pass     string
		want     []string
	}{
		{"the weekend waits for Monday", plain, "2026-11-07 09:00", "2026-11-09 07:00", []string{"2026-11-09T06:00"}},
		{"a holiday Monday waits for Tuesday", closed, "2026-11-07 09:00", "2026-11-09 12:00", []string{}},
		{"the first minute of the month", plain, "2026-11-30 23:30", "2026-12-01 00:00", []string{"2026-12-01T00:00"}},
	} {
		resetSessions(t, s)
		resolved, err := time.ParseInLocation("2006-01-02 15:04", c.resolved, clock.Lagos)
		must(t, err)
		pass, err := time.ParseInLocation("2006-01-02 15:04", c.pass, clock.Lagos)
		must(t, err)
		x := transfer("w", "999002", resolved.Add(-time.Minute))
		_, err = begin(ctx, s, x)
		must(t, err)
		must(t, s.Resolve(ctx, "w", SessionOK, nip.OutcomeCredited, resolved))
		if got := settleKeys(t, s, c.sched, pass); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	resetSessions(t, s)
	pay(t, s, "ad1", "999001", "999002", 500000, SessionOK, time.Minute)
	now := t0.Add(time.Hour)
	got, err := s.Settle(ctx, Schedule{Per: 4}, now, true)
	if err != nil || len(got) != 1 || got[0].Key != "adhoc:"+now.In(clock.Lagos).Format(time.RFC3339Nano)+":ad1" || !got[0].CutoffAt.Equal(now) {
		t.Fatalf("on demand: %+v, %v", got, err)
	}
}

func settlementProblems(t *testing.T, s *Store) {
	sched := Schedule{Per: 4}
	noon := time.Date(2026, 11, 4, 12, 0, 0, 0, t0.Location())
	for _, r := range []struct {
		name       string
		resolvedAt []time.Duration
		settled    bool
		checkedAt  time.Time
		want       []string
	}{
		{"inside the grace", []time.Duration{0, time.Hour}, false, noon.Add(30 * time.Second), []string{}},
		{"two transfers past the grace", []time.Duration{0, time.Hour}, false, noon.Add(61 * time.Second), []string{"2 transfers resolved before the 2026-11-04 12:00 cutoff are not settled yet"}},
		{"one transfer past the grace", []time.Duration{0, 4 * time.Hour}, false, noon.Add(61 * time.Second), []string{"1 transfer resolved before the 2026-11-04 12:00 cutoff is not settled yet"}},
		{"settled but not posted", []time.Duration{0}, true, noon.Add(time.Hour), []string{"settlement session 2026-11-04T12:00 was cut off at 2026-11-04T12:00:00+01:00 and is not posted to the CBN yet"}},
	} {
		resetSessions(t, s)
		for i, d := range r.resolvedAt {
			pay(t, s, fmt.Sprintf("transfer-%d", i+1), "999001", "999002", 500000, SessionOK, d)
		}
		if r.settled {
			_, err := s.Settle(t.Context(), sched, noon.Add(time.Minute), false)
			must(t, err)
		}
		if got, err := s.SettlementProblems(t.Context(), sched, r.checkedAt); err != nil || !reflect.DeepEqual(got, r.want) {
			t.Errorf("%s: %q, %v, want %q", r.name, got, err, r.want)
		}
	}
}
