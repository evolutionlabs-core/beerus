package connect

import (
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/countrypack"
)

func TestSessionSchedule(t *testing.T) {
	s := schedule{per: 12}
	at := func(day string, hh, mm int) time.Time {
		d, err := clock.ParseDate(day)
		if err != nil {
			t.Fatal(err)
		}
		return d.Start().Add(time.Duration(hh)*time.Hour + time.Duration(mm)*time.Minute)
	}
	for _, c := range []struct {
		booked time.Time
		key    string
	}{
		{at("2026-11-02", 0, 0), "2026-11-02T02:00"},
		{at("2026-11-03", 0, 0), "2026-11-03T00:00"},
		{at("2026-11-02", 0, 1), "2026-11-02T02:00"},
		{at("2026-11-02", 2, 0), "2026-11-02T02:00"},
		{at("2026-11-02", 2, 1), "2026-11-02T04:00"},
		{at("2026-11-02", 23, 59), "2026-11-03T00:00"},
		{at("2026-11-06", 23, 0), "2026-11-07T00:00"},
		{at("2026-11-07", 9, 0), "2026-11-09T02:00"},
		{at("2026-11-08", 23, 59), "2026-11-09T02:00"},
		{at("2026-11-09", 1, 30), "2026-11-09T02:00"},
	} {
		got := s.key(s.next(c.booked.Add(-time.Nanosecond)))
		if got != c.key {
			t.Errorf("a transfer booked at %s settles in %s, want %s", c.booked.Format(time.RFC3339), got, c.key)
		}
	}
	if got := s.next(at("2026-11-06", 22, 0)); !got.Equal(at("2026-11-07", 0, 0)) {
		t.Errorf("last Friday session closes at %s", got)
	}
	if got := s.next(at("2026-11-07", 0, 0)); !got.Equal(at("2026-11-09", 2, 0)) {
		t.Errorf("first session after Friday closes at %s", got)
	}
	four := (&App{cfg: &config.Config{}}).schedule()
	for _, c := range []struct {
		booked time.Time
		key    string
	}{
		{at("2026-11-02", 5, 59), "2026-11-02T06:00"},
		{at("2026-11-02", 6, 1), "2026-11-02T12:00"},
		{at("2026-11-02", 23, 0), "2026-11-03T00:00"},
		{at("2026-11-07", 9, 0), "2026-11-09T06:00"},
	} {
		if got := four.key(four.next(c.booked.Add(-time.Nanosecond))); got != c.key {
			t.Errorf("with the default schedule, a transfer booked at %s settles in %s, want %s", c.booked.Format(time.RFC3339), got, c.key)
		}
	}
}

func TestSessionScheduleSkipsHolidays(t *testing.T) {
	pack, err := countrypack.Nigeria()
	if err != nil {
		t.Fatal(err)
	}
	s := schedule{per: 12, cal: clock.NewCalendar(pack.Fixed.Days, nil, nil)}
	at := func(day string, hh int) time.Time {
		d, err := clock.ParseDate(day)
		if err != nil {
			t.Fatal(err)
		}
		return d.Start().Add(time.Duration(hh) * time.Hour)
	}
	for _, c := range []struct {
		name   string
		booked time.Time
		want   time.Time
	}{
		{"last session of 30 Sep", at("2026-09-30", 23), at("2026-10-01", 0)},
		{"midnight session of Independence Day", at("2026-10-01", 0).Add(-time.Nanosecond), at("2026-10-01", 0)},
		{"first session after Independence Day", at("2026-10-01", 0), at("2026-10-02", 2)},
		{"transfer on Independence Day", at("2026-10-01", 10), at("2026-10-02", 2)},
		{"Good Friday is a business day", at("2026-04-03", 0), at("2026-04-03", 2)},
		{"Christmas Day then the weekend", at("2026-12-24", 23), at("2026-12-25", 0)},
		{"first session after Christmas Day", at("2026-12-25", 0), at("2026-12-28", 2)},
		{"New Year's Day then the weekend", at("2027-01-01", 5), at("2027-01-04", 2)},
	} {
		if got := s.next(c.booked); !got.Equal(c.want) {
			t.Errorf("%s: next session after %s closes at %s, want %s", c.name, c.booked.Format(time.RFC3339), got.Format(time.RFC3339), c.want.Format(time.RFC3339))
		}
	}
}

func TestScheduleFollowsCalendar(t *testing.T) {
	friday := clock.Date{Y: 2026, M: 11, D: 6}
	plain := schedule{per: 12}
	closed := schedule{per: 12, cal: clock.NewCalendar(nil, []clock.Date{friday.AddDays(3)}, nil)}
	from := friday.AddDays(2).Start()
	if got := plain.next(from); !got.Equal(friday.AddDays(3).Start().Add(2 * time.Hour)) {
		t.Fatalf("plain next session %s", got)
	}
	if got := closed.next(from); !got.Equal(friday.AddDays(4).Start().Add(2 * time.Hour)) {
		t.Errorf("next session with the extra holiday %s", got)
	}
}

func TestPreviousSession(t *testing.T) {
	s := schedule{per: 4}
	at := func(day string, hh, mm int) time.Time {
		d, err := clock.ParseDate(day)
		if err != nil {
			t.Fatal(err)
		}
		return d.Start().Add(time.Duration(hh)*time.Hour + time.Duration(mm)*time.Minute)
	}
	for _, c := range []struct{ now, want time.Time }{
		{at("2026-11-03", 10, 0), at("2026-11-03", 6, 0)},
		{at("2026-11-03", 6, 0), at("2026-11-03", 6, 0)},
		{at("2026-11-03", 0, 0), at("2026-11-03", 0, 0)},
		{at("2026-11-03", 0, 1), at("2026-11-03", 0, 0)},
		{at("2026-11-03", 5, 59), at("2026-11-03", 0, 0)},
		{at("2026-11-07", 12, 0), at("2026-11-07", 0, 0)},
		{at("2026-11-08", 12, 0), at("2026-11-07", 0, 0)},
		{at("2026-11-09", 1, 0), at("2026-11-07", 0, 0)},
	} {
		got := s.prev(c.now)
		if !got.Equal(c.want) {
			t.Errorf("the session before %s is %s, want %s", c.now.Format(time.RFC3339), got.Format(time.RFC3339), c.want.Format(time.RFC3339))
		}
		if next := s.next(got.Add(-time.Nanosecond)); !next.Equal(got) {
			t.Errorf("%s is not a session cutoff", got.Format(time.RFC3339))
		}
	}
}
