package nibss

import (
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/countrypack"
)

func at(t *testing.T, s string) time.Time {
	t.Helper()
	got, err := time.ParseInLocation("2006-01-02 15:04", s, clock.Lagos)
	must(t, err)
	return got
}

func TestScheduleNextAndPrev(t *testing.T) {
	pack, err := countrypack.Nigeria()
	must(t, err)
	twelve, four := Schedule{Per: 12}, Schedule{Per: 4}
	holidays := Schedule{Per: 12, Cal: clock.NewCalendar(pack.Fixed.Days, nil, nil)}
	closed := Schedule{Per: 12, Cal: clock.NewCalendar(nil, []clock.Date{{Y: 2026, M: 11, D: 9}}, nil)}
	for _, c := range []struct {
		name         string
		sched        Schedule
		booked, want string
	}{
		{"on a cutoff", twelve, "2026-11-02 02:00", "2026-11-02 02:00"},
		{"just after a cutoff", twelve, "2026-11-02 02:01", "2026-11-02 04:00"},
		{"the last minute of the day", twelve, "2026-11-02 23:59", "2026-11-03 00:00"},
		{"the default four a day", four, "2026-11-02 06:01", "2026-11-02 12:00"},
		{"Saturday waits for Monday", twelve, "2026-11-07 09:00", "2026-11-09 02:00"},
		{"a holiday Monday waits for Tuesday", closed, "2026-11-08 12:00", "2026-11-10 02:00"},
		{"Independence Day waits for the next day", holidays, "2026-10-01 10:00", "2026-10-02 02:00"},
	} {
		if got := c.sched.Next(at(t, c.booked).Add(-time.Nanosecond)); !got.Equal(at(t, c.want)) {
			t.Errorf("%s: next session after %s closes at %s, want %s", c.name, c.booked, got.Format(time.RFC3339), c.want)
		}
	}
	for _, c := range []struct{ now, want string }{
		{"2026-11-03 06:00", "2026-11-03 06:00"},
		{"2026-11-03 05:59", "2026-11-03 00:00"},
		{"2026-11-09 01:00", "2026-11-07 00:00"},
	} {
		got := four.Prev(at(t, c.now))
		if !got.Equal(at(t, c.want)) || !four.Next(got.Add(-time.Nanosecond)).Equal(got) {
			t.Errorf("the session before %s is %s, want %s", c.now, got.Format(time.RFC3339), c.want)
		}
	}
}
