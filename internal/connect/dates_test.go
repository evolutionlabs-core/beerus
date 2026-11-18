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
