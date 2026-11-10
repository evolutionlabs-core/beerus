package connect

import (
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
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
	s := schedule{per: 12}
	day := func(v string) clock.Date {
		d, err := clock.ParseDate(v)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	if got := s.next(day("2026-09-30").Start().Add(23 * time.Hour)); !got.Equal(day("2026-10-01").Start()) {
		t.Errorf("last session of 30 Sep closes at %s", got)
	}
	if got := s.next(day("2026-10-01").Start().Add(-time.Nanosecond)); !got.Equal(day("2026-10-01").Start()) {
		t.Errorf("midnight session closes at %s", got)
	}
	if got := s.next(day("2026-10-01").Start()); !got.Equal(day("2026-10-02").Start().Add(2 * time.Hour)) {
		t.Errorf("first session after Independence Day closes at %s", got)
	}
	if got := s.next(day("2026-10-01").Start().Add(10 * time.Hour)); !got.Equal(day("2026-10-02").Start().Add(2 * time.Hour)) {
		t.Errorf("a transfer on Independence Day settles at %s", got)
	}
	if got := s.next(day("2026-04-02").Start().Add(23 * time.Hour)); !got.Equal(day("2026-04-03").Start()) {
		t.Errorf("last session before Good Friday closes at %s", got)
	}
	if got := s.next(day("2026-04-03").Start()); !got.Equal(day("2026-04-07").Start().Add(2 * time.Hour)) {
		t.Errorf("first session after Easter Monday closes at %s", got)
	}
}

func TestScheduleFollowsCalendar(t *testing.T) {
	friday := clock.Date{Y: 2026, M: 11, D: 6}
	plain := schedule{per: 12}
	closed := schedule{per: 12, cal: clock.NewCalendar([]clock.Date{friday.AddDays(3)}, nil)}
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

func TestNoBankTakesTheCBNDatabase(t *testing.T) {
	cfg := &config.Config{Dir: t.TempDir()}
	a := &App{cfg: cfg}
	_, err := a.admit(BankRequest{Name: "Kano Digital"}, 7450, config.DatabaseName(cfg.Dir, cbnID), nil)
	if err == nil || !strings.Contains(err.Error(), "belongs to the CBN") {
		t.Fatalf("a bank on the CBN database: %v", err)
	}
}
