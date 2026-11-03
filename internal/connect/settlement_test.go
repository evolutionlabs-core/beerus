package connect

import (
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
