package connect

import (
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

func day(y int, m time.Month, d int) clock.Date {
	return clock.Date{Y: y, M: m, D: d}
}

func TestHistoryStart(t *testing.T) {
	today := day(2026, time.November, 14)
	rows := []struct {
		name   string
		start  clock.Date
		today  clock.Date
		days   int
		want   clock.Date
		capped bool
	}{
		{"inside the history", day(2026, time.October, 1), today, 90, day(2026, time.October, 1), false},
		{"on the floor", day(2026, time.August, 16), today, 90, day(2026, time.August, 16), false},
		{"a day before the floor", day(2026, time.August, 15), today, 90, day(2026, time.August, 16), true},
		{"far in the past", day(2000, time.January, 1), today, 90, day(2026, time.August, 16), true},
		{"a start in the future", day(2026, time.December, 1), today, 90, day(2026, time.December, 1), false},
		{"floor on a leap day", day(2028, time.February, 28), day(2028, time.March, 29), 29, day(2028, time.February, 29), true},
		{"floor across a leap year", day(2027, time.March, 1), day(2028, time.March, 1), 366, day(2027, time.March, 1), false},
		{"floor across year end", day(2025, time.December, 1), day(2026, time.January, 5), 30, day(2025, time.December, 6), true},
		{"far-future today", day(2100, time.January, 1), day(2126, time.March, 1), 730, day(2124, time.March, 1), true},
		{"no history", day(2026, time.November, 13), today, 0, today, true},
	}
	for _, r := range rows {
		got, capped := historyStart(r.start, r.today, r.days)
		if got != r.want || capped != r.capped {
			t.Errorf("%s: %s capped %v, want %s capped %v", r.name, got, capped, r.want, r.capped)
		}
	}
}

func TestReconnectFrom(t *testing.T) {
	rows := []struct {
		name    string
		current clock.Date
		lines   []clock.Date
		want    clock.Date
	}{
		{"no earlier line", day(2026, time.October, 10), []clock.Date{day(2026, time.October, 10)}, day(2026, time.October, 10)},
		{"a later start does not move it", day(2026, time.October, 10), []clock.Date{day(2026, time.November, 1)}, day(2026, time.October, 10)},
		{"an earlier start moves it back", day(2026, time.October, 10), []clock.Date{day(2026, time.September, 30)}, day(2026, time.September, 30)},
		{"lines on several dates keep the earliest", day(2026, time.October, 10), []clock.Date{day(2026, time.October, 5), day(2026, time.September, 1), day(2026, time.October, 1), day(2026, time.November, 1)}, day(2026, time.September, 1)},
		{"across a leap day", day(2028, time.March, 1), []clock.Date{day(2028, time.February, 29), day(2028, time.February, 28)}, day(2028, time.February, 28)},
		{"across year end", day(2027, time.January, 1), []clock.Date{day(2026, time.December, 31)}, day(2026, time.December, 31)},
		{"far future", day(2126, time.March, 1), []clock.Date{day(2126, time.March, 2)}, day(2126, time.March, 1)},
	}
	for _, r := range rows {
		got := r.current
		for _, eff := range r.lines {
			got = reconnectFrom(got, eff)
		}
		if got != r.want {
			t.Errorf("%s: %s, want %s", r.name, got, r.want)
		}
	}
}

func TestAsOfBounds(t *testing.T) {
	rows := []struct {
		name     string
		from     clock.Date
		today    clock.Date
		min, max clock.Date
	}{
		{"mid month", day(2026, time.October, 10), day(2026, time.November, 14), day(2026, time.October, 9), day(2026, time.November, 13)},
		{"month start", day(2026, time.March, 1), day(2026, time.April, 1), day(2026, time.February, 28), day(2026, time.March, 31)},
		{"leap year", day(2028, time.March, 1), day(2028, time.March, 1), day(2028, time.February, 29), day(2028, time.February, 29)},
		{"year start", day(2027, time.January, 1), day(2027, time.January, 1), day(2026, time.December, 31), day(2026, time.December, 31)},
		{"far future", day(2126, time.March, 1), day(2126, time.March, 2), day(2126, time.February, 28), day(2126, time.March, 1)},
	}
	for _, r := range rows {
		min, max := asOfBounds(r.from, r.today)
		if min != r.min || max != r.max {
			t.Errorf("%s: %s to %s, want %s to %s", r.name, min, max, r.min, r.max)
		}
	}
}
