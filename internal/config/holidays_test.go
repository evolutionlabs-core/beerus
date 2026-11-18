package config

import (
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

func TestHolidayOverrides(t *testing.T) {
	over, err := NIBSSFile{HolidaysAdd: []string{"2026-05-29"}, HolidaysRemove: []string{"2026-10-01"}}.Calendar()
	if err != nil {
		t.Fatal(err)
	}
	plain, err := NIBSSFile{}.Calendar()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		cal  clock.Calendar
		date clock.Date
		want bool
	}{
		{"added day", over, clock.Date{Y: 2026, M: 5, D: 29}, true},
		{"removed day", over, clock.Date{Y: 2026, M: 10, D: 1}, false},
		{"fixed day with overrides", over, clock.Date{Y: 2026, M: 12, D: 25}, true},
		{"fixed day without overrides", plain, clock.Date{Y: 2026, M: 10, D: 1}, true},
		{"ordinary day without overrides", plain, clock.Date{Y: 2026, M: 5, D: 29}, false},
	} {
		if got := c.cal.IsHoliday(c.date); got != c.want {
			t.Errorf("%s: IsHoliday = %t, want %t", c.name, got, c.want)
		}
	}
	for _, c := range []Config{{Holidays: &Holidays{Add: []string{"soon"}}}, {Holidays: &Holidays{Remove: []string{"soon"}}}} {
		if _, err := finish(c); err == nil {
			t.Error("a bad date should be rejected")
		}
	}
}
