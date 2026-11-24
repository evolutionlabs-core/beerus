package nibss

import (
	"reflect"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/countrypack"
)

func at(t *testing.T, day string, hh, mm int) time.Time {
	t.Helper()
	d, err := clock.ParseDate(day)
	if err != nil {
		t.Fatal(err)
	}
	return d.Start().Add(time.Duration(hh)*time.Hour + time.Duration(mm)*time.Minute)
}

func TestScheduleNext(t *testing.T) {
	pack, err := countrypack.Nigeria()
	if err != nil {
		t.Fatal(err)
	}
	twelve := Schedule{Per: 12}
	four := Schedule{Per: config.DefaultNIPSessions}
	holidays := Schedule{Per: 12, Cal: clock.NewCalendar(pack.Fixed.Days, nil, nil)}
	monday := clock.Date{Y: 2026, M: 11, D: 9}
	closed := Schedule{Per: 12, Cal: clock.NewCalendar(nil, []clock.Date{monday}, nil)}
	for _, c := range []struct {
		name   string
		sched  Schedule
		booked time.Time
		want   time.Time
	}{
		{"just after midnight", twelve, at(t, "2026-11-02", 0, 1), at(t, "2026-11-02", 2, 0)},
		{"midnight", twelve, at(t, "2026-11-03", 0, 0), at(t, "2026-11-03", 0, 0)},
		{"on a cutoff", twelve, at(t, "2026-11-02", 2, 0), at(t, "2026-11-02", 2, 0)},
		{"just after a cutoff", twelve, at(t, "2026-11-02", 2, 1), at(t, "2026-11-02", 4, 0)},
		{"last minute of the day", twelve, at(t, "2026-11-02", 23, 59), at(t, "2026-11-03", 0, 0)},
		{"Friday night", twelve, at(t, "2026-11-06", 23, 0), at(t, "2026-11-07", 0, 0)},
		{"Saturday", twelve, at(t, "2026-11-07", 9, 0), at(t, "2026-11-09", 2, 0)},
		{"end of Sunday", twelve, at(t, "2026-11-08", 23, 59), at(t, "2026-11-09", 2, 0)},
		{"early Monday", twelve, at(t, "2026-11-09", 1, 30), at(t, "2026-11-09", 2, 0)},
		{"default schedule before six", four, at(t, "2026-11-02", 5, 59), at(t, "2026-11-02", 6, 0)},
		{"default schedule after six", four, at(t, "2026-11-02", 6, 1), at(t, "2026-11-02", 12, 0)},
		{"default schedule at night", four, at(t, "2026-11-02", 23, 0), at(t, "2026-11-03", 0, 0)},
		{"default schedule on a Saturday", four, at(t, "2026-11-07", 9, 0), at(t, "2026-11-09", 6, 0)},
		{"Sunday before a closed Monday", closed, at(t, "2026-11-08", 12, 0), at(t, "2026-11-10", 2, 0)},
		{"Sunday before an open Monday", twelve, at(t, "2026-11-08", 12, 0), at(t, "2026-11-09", 2, 0)},
		{"last session of 30 September", holidays, at(t, "2026-09-30", 23, 0), at(t, "2026-10-01", 0, 0)},
		{"midnight session of Independence Day", holidays, at(t, "2026-10-01", 0, 0).Add(-time.Nanosecond), at(t, "2026-10-01", 0, 0)},
		{"first session after Independence Day", holidays, at(t, "2026-10-01", 0, 0), at(t, "2026-10-02", 2, 0)},
		{"transfer on Independence Day", holidays, at(t, "2026-10-01", 10, 0), at(t, "2026-10-02", 2, 0)},
		{"Good Friday is a business day", holidays, at(t, "2026-04-03", 0, 0), at(t, "2026-04-03", 2, 0)},
		{"Christmas Eve", holidays, at(t, "2026-12-24", 23, 0), at(t, "2026-12-25", 0, 0)},
		{"first session after Christmas Day", holidays, at(t, "2026-12-25", 0, 0), at(t, "2026-12-28", 2, 0)},
		{"New Year's Day then the weekend", holidays, at(t, "2027-01-01", 5, 0), at(t, "2027-01-04", 2, 0)},
		{"last session of the year", holidays, at(t, "2026-12-31", 23, 0), at(t, "2027-01-01", 0, 0)},
		{"first session of the year is a holiday", holidays, at(t, "2027-01-01", 0, 0), at(t, "2027-01-04", 2, 0)},
		{"month end before a weekend", holidays, at(t, "2026-10-30", 23, 0), at(t, "2026-10-31", 0, 0)},
		{"month end on a Saturday", holidays, at(t, "2026-10-31", 9, 0), at(t, "2026-11-02", 2, 0)},
		{"leap day", holidays, at(t, "2028-02-29", 23, 0), at(t, "2028-03-01", 0, 0)},
		{"day after a leap day", holidays, at(t, "2028-03-01", 0, 0), at(t, "2028-03-01", 2, 0)},
		{"month end on a Sunday", holidays, at(t, "2027-02-28", 1, 0), at(t, "2027-03-01", 2, 0)},
	} {
		booked := c.booked
		if reflect.DeepEqual(c.sched.Cal, clock.Calendar{}) {
			booked = booked.Add(-time.Nanosecond)
		}
		if got := c.sched.Next(booked); !got.Equal(c.want) {
			t.Errorf("%s: next session after %s closes at %s, want %s", c.name, c.booked.Format(time.RFC3339), got.Format(time.RFC3339), c.want.Format(time.RFC3339))
		}
	}
}

func TestSchedulePrev(t *testing.T) {
	s := Schedule{Per: 4}
	for _, c := range []struct{ now, want time.Time }{
		{at(t, "2026-11-03", 10, 0), at(t, "2026-11-03", 6, 0)},
		{at(t, "2026-11-03", 6, 0), at(t, "2026-11-03", 6, 0)},
		{at(t, "2026-11-03", 0, 0), at(t, "2026-11-03", 0, 0)},
		{at(t, "2026-11-03", 0, 1), at(t, "2026-11-03", 0, 0)},
		{at(t, "2026-11-03", 5, 59), at(t, "2026-11-03", 0, 0)},
		{at(t, "2026-11-07", 12, 0), at(t, "2026-11-07", 0, 0)},
		{at(t, "2026-11-08", 12, 0), at(t, "2026-11-07", 0, 0)},
		{at(t, "2026-11-09", 1, 0), at(t, "2026-11-07", 0, 0)},
	} {
		got := s.Prev(c.now)
		if !got.Equal(c.want) || !s.Next(got.Add(-time.Nanosecond)).Equal(got) {
			t.Errorf("the session before %s is %s, want %s", c.now.Format(time.RFC3339), got.Format(time.RFC3339), c.want.Format(time.RFC3339))
		}
	}
}
