package config

import (
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

func TestHolidayOverrides(t *testing.T) {
	over, err := finish(Config{Holidays: &Holidays{Add: []string{"2026-05-29"}, Remove: []string{"2026-10-01"}}})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := finish(Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		cal  clock.Calendar
		date clock.Date
		want bool
	}{
		{"added day", over.Calendar(), clock.Date{Y: 2026, M: 5, D: 29}, true},
		{"removed day", over.Calendar(), clock.Date{Y: 2026, M: 10, D: 1}, false},
		{"fixed day with overrides", over.Calendar(), clock.Date{Y: 2026, M: 12, D: 25}, true},
		{"fixed day without overrides", plain.Calendar(), clock.Date{Y: 2026, M: 10, D: 1}, true},
		{"ordinary day without overrides", plain.Calendar(), clock.Date{Y: 2026, M: 5, D: 29}, false},
	} {
		if got := c.cal.IsHoliday(c.date); got != c.want {
			t.Errorf("%s: IsHoliday = %t, want %t", c.name, got, c.want)
		}
	}
	if _, err := finish(Config{Holidays: &Holidays{Add: []string{"soon"}}}); err == nil {
		t.Error("a bad date should be rejected")
	}
}
