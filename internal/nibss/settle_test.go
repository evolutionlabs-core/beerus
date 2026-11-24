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

func settleByCutoff(t *testing.T, s *Store) {
	ctx := t.Context()
	sched := Schedule{Per: 4}
	for i, id := range []string{"v1", "v2", "v3", "v4"} {
		if _, err := begin(ctx, s, transfer(id, "999002", t0)); err != nil {
			t.Fatal(err)
		}
		if err := s.Resolve(ctx, id, SessionOK, nip.OutcomeCredited, t0.Add(time.Duration(i*i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	banks := map[string]string{"999001": "bank-a", "999002": "bank-b"}
	type pass struct {
		name  string
		at    time.Duration
		adhoc bool
		keys  []string
		nets  []BankNet
	}
	passes := []pass{
		{"before the first cutoff", 2 * time.Hour, false, []string{}, nil},
		{"the cutoffs that have passed", 11 * time.Hour, false, []string{"2026-11-04T12:00", "2026-11-04T18:00"}, []BankNet{{"bank-a", -1000000}, {"bank-b", 1000000}}},
		{"the same pass again", 11 * time.Hour, false, []string{"2026-11-04T12:00", "2026-11-04T18:00"}, nil},
	}
	for _, p := range passes {
		got, err := s.Settle(ctx, sched, t0.Add(p.at), p.adhoc)
		keys := []string{}
		for _, x := range got {
			keys = append(keys, x.Key)
		}
		if err != nil || !reflect.DeepEqual(keys, p.keys) {
			t.Fatalf("%s: %v, %v, want %v", p.name, keys, err, p.keys)
		}
		if len(got) > 0 && p.nets != nil {
			nets, err := got[0].Nets(banks)
			if err != nil || !reflect.DeepEqual(nets, p.nets) || len(Moved(nets)) != 2 {
				t.Fatalf("%s: nets %+v, %v, want %+v", p.name, nets, err, p.nets)
			}
		}
	}
	if _, err := (Settlement{Key: "k", Sessions: []Session{transfer("x", "999002", t0)}}).Nets(map[string]string{"999001": "bank-a"}); err == nil || !strings.Contains(err.Error(), "session x is between 999001 and 999002, and one of them is not in the directory") {
		t.Fatalf("nets with a bank that is not in the directory: %v", err)
	}
	if got := Moved([]BankNet{{"bank-a", -5}, {"bank-b", 0}, {"bank-c", 5}}); !reflect.DeepEqual(got, []BankNet{{"bank-a", -5}, {"bank-c", 5}}) {
		t.Fatalf("moved: %+v", got)
	}
	for _, key := range []string{"2026-11-04T12:00", "2026-11-04T18:00"} {
		if err := s.MarkPosted(ctx, key, t0); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.PostedCount(ctx); err != nil || n != 2 {
		t.Fatalf("posted %d, %v", n, err)
	}
	got, err := s.Settle(ctx, sched, t0.Add(11*time.Hour), false)
	if err != nil || len(got) != 0 {
		t.Fatalf("after posting: %+v, %v", got, err)
	}
	if err := s.KeepSchedule(ctx, 4); err != nil {
		t.Fatal(err)
	}
	if err := s.KeepSchedule(ctx, 4); err != nil {
		t.Fatalf("keeping the same schedule twice: %v", err)
	}
	if err := s.KeepSchedule(ctx, 12); err == nil || err.Error() != "nip_sessions changed: NIBSS has settled with 4 sessions a day, so nip_sessions must stay 4" {
		t.Fatalf("a new schedule: %v", err)
	}
}

func settleOnTheCalendar(t *testing.T, s *Store) {
	ctx := t.Context()
	at := func(day string, hh, mm int) time.Time {
		d, err := clock.ParseDate(day)
		if err != nil {
			t.Fatal(err)
		}
		return d.Start().Add(time.Duration(hh)*time.Hour + time.Duration(mm)*time.Minute)
	}
	monday := clock.Date{Y: 2026, M: time.November, D: 9}
	for _, c := range []struct {
		name     string
		sched    Schedule
		resolved []time.Time
		passes   []time.Time
		keys     [][]string
	}{
		{
			name:     "weekend",
			sched:    Schedule{Per: 4},
			resolved: []time.Time{at("2026-11-06", 23, 0), at("2026-11-07", 9, 0)},
			passes:   []time.Time{at("2026-11-07", 12, 0), at("2026-11-08", 12, 0), at("2026-11-09", 7, 0)},
			keys:     [][]string{{"2026-11-07T00:00"}, {"2026-11-07T00:00"}, {"2026-11-07T00:00", "2026-11-09T06:00"}},
		},
		{
			name:     "holiday on the Monday",
			sched:    Schedule{Per: 4, Cal: clock.NewCalendar(nil, []clock.Date{monday}, nil)},
			resolved: []time.Time{at("2026-11-07", 9, 0)},
			passes:   []time.Time{at("2026-11-09", 12, 0), at("2026-11-10", 7, 0)},
			keys:     [][]string{{}, {"2026-11-10T06:00"}},
		},
		{
			name:     "month end",
			sched:    Schedule{Per: 4},
			resolved: []time.Time{at("2026-11-30", 23, 30)},
			passes:   []time.Time{at("2026-11-30", 23, 59), at("2026-12-01", 0, 0)},
			keys:     [][]string{{}, {"2026-12-01T00:00"}},
		},
		{
			name:     "year end",
			sched:    Schedule{Per: 4},
			resolved: []time.Time{at("2026-12-31", 23, 30)},
			passes:   []time.Time{at("2026-12-31", 23, 59), at("2027-01-01", 0, 0)},
			keys:     [][]string{{}, {"2027-01-01T00:00"}},
		},
		{
			name:     "leap day",
			sched:    Schedule{Per: 4},
			resolved: []time.Time{at("2028-02-29", 23, 30)},
			passes:   []time.Time{at("2028-03-01", 0, 0)},
			keys:     [][]string{{"2028-03-01T00:00"}},
		},
		{
			name:     "far-future year",
			sched:    Schedule{Per: 4},
			resolved: []time.Time{at("2126-03-01", 7, 0)},
			passes:   []time.Time{at("2126-03-01", 12, 0)},
			keys:     [][]string{{"2126-03-01T12:00"}},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			resetSessions(t, s)
			for i, resolved := range c.resolved {
				id := "w" + string(rune('a'+i))
				if _, err := begin(ctx, s, transfer(id, "999002", resolved.Add(-time.Minute))); err != nil {
					t.Fatal(err)
				}
				if err := s.Resolve(ctx, id, SessionOK, nip.OutcomeCredited, resolved); err != nil {
					t.Fatal(err)
				}
			}
			for i, now := range c.passes {
				got, err := s.Settle(ctx, c.sched, now, false)
				keys := []string{}
				for _, x := range got {
					keys = append(keys, x.Key)
					if strings.HasPrefix(x.Key, "adhoc:") {
						t.Errorf("pass %d: a due cutoff was settled as %s", i, x.Key)
					}
				}
				if err != nil || !reflect.DeepEqual(keys, c.keys[i]) {
					t.Fatalf("pass %d at %s: %v, %v, want %v", i, now.Format(time.RFC3339), keys, err, c.keys[i])
				}
			}
			last := c.passes[len(c.passes)-1]
			again, err := s.Settle(ctx, c.sched, last, false)
			if err != nil {
				t.Fatal(err)
			}
			total := 0
			for _, x := range again {
				total += len(x.Sessions)
			}
			if total != len(c.resolved) {
				t.Fatalf("settling the same cutoffs twice put %d sessions into settlements, want %d", total, len(c.resolved))
			}
			if left, err := s.Unsettled(ctx, last); err != nil || len(left) != 0 {
				t.Fatalf("unsettled after the passes: %v, %v", left, err)
			}
		})
	}
}

func settleOnDemand(t *testing.T, s *Store) {
	ctx := t.Context()
	if _, err := begin(ctx, s, transfer("ad1", "999002", t0)); err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(ctx, "ad1", SessionOK, nip.OutcomeCredited, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	now := t0.Add(time.Hour)
	got, err := s.Settle(ctx, Schedule{Per: 4}, now, true)
	if err != nil || len(got) != 1 || got[0].Key != "adhoc:"+now.In(clock.Lagos).Format(time.RFC3339Nano)+":ad1" || !got[0].CutoffAt.Equal(now) {
		t.Fatalf("on demand: %+v, %v", got, err)
	}
}

func settlementProblems(t *testing.T, s *Store) {
	ctx := t.Context()
	sched := Schedule{Per: 4}
	noon := time.Date(2026, 11, 4, 12, 0, 0, 0, t0.Location())
	for _, r := range []struct {
		name       string
		resolvedAt []time.Duration
		settled    bool
		checkedAt  time.Time
		want       []string
	}{
		{"nothing yet", nil, false, noon.Add(time.Hour), []string{}},
		{"inside the grace", []time.Duration{0, time.Hour}, false, noon.Add(30 * time.Second), []string{}},
		{"two transfers past the grace", []time.Duration{0, time.Hour}, false, noon.Add(61 * time.Second), []string{"2 transfers resolved before the 2026-11-04 12:00 cutoff are not settled yet"}},
		{"one transfer past the grace", []time.Duration{0, 4 * time.Hour}, false, noon.Add(61 * time.Second), []string{"1 transfer resolved before the 2026-11-04 12:00 cutoff is not settled yet"}},
		{"settled but not posted", []time.Duration{0}, true, noon.Add(time.Hour), []string{"settlement session 2026-11-04T12:00 was cut off at 2026-11-04T12:00:00+01:00 and is not posted to the CBN yet"}},
		{"settled inside the grace", []time.Duration{0}, true, noon.Add(30 * time.Second), []string{}},
	} {
		resetSessions(t, s)
		for i, d := range r.resolvedAt {
			pay(t, s, fmt.Sprintf("transfer-%d", i+1), "999001", "999002", 500000, SessionOK, d)
		}
		if r.settled {
			if _, err := s.Settle(ctx, sched, noon.Add(time.Minute), false); err != nil {
				t.Fatal(err)
			}
		}
		if got, err := s.SettlementProblems(ctx, sched, r.checkedAt); err != nil || !reflect.DeepEqual(got, r.want) {
			t.Errorf("%s: %q, %v, want %q", r.name, got, err, r.want)
		}
	}
}
