package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

func load(t *testing.T, extra string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	body := "seed: 7\ngenesis: 2025-01-01\n" + extra
	if err := os.WriteFile(filepath.Join(dir, "beerus.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(dir)
}

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
