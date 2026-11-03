package config

import (
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

func TestHolidayOverrides(t *testing.T) {
	c, err := load(t, "holidays:\n  add: [2026-05-29]\n  remove: [2026-05-27]\n")
	if err != nil {
		t.Fatal(err)
	}
	cal := c.Calendar()
	if cal.IsHoliday(clock.Date{Y: 2026, M: 5, D: 27}) || !cal.IsHoliday(clock.Date{Y: 2026, M: 5, D: 29}) {
		t.Error("the overrides in beerus.yaml were not applied")
	}
	plain, err := load(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if !plain.Calendar().IsHoliday(clock.Date{Y: 2026, M: 5, D: 27}) {
		t.Error("without overrides the rules should apply")
	}
	if _, err := load(t, "holidays:\n  add: [soon]\n"); err == nil {
		t.Error("a bad date should be rejected")
	}
}
