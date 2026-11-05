package config

import (
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

func TestHolidayOverrides(t *testing.T) {
	c, err := finish(Config{Holidays: &Holidays{Add: []string{"2026-05-29"}, Remove: []string{"2026-05-27"}}})
	if err != nil {
		t.Fatal(err)
	}
	cal := c.Calendar()
	if cal.IsHoliday(clock.Date{Y: 2026, M: 5, D: 27}) || !cal.IsHoliday(clock.Date{Y: 2026, M: 5, D: 29}) {
		t.Error("the holiday overrides were not applied")
	}
	plain, err := finish(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if !plain.Calendar().IsHoliday(clock.Date{Y: 2026, M: 5, D: 27}) {
		t.Error("without overrides the rules should apply")
	}
	if _, err := finish(Config{Holidays: &Holidays{Add: []string{"soon"}}}); err == nil {
		t.Error("a bad date should be rejected")
	}
}
