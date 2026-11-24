package core

import (
	"log/slog"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

func newTestApp(t *testing.T) *App {
	t.Helper()
	cfg := initTestConfig(t, clock.Date{Y: 2026, M: 11, D: 3})
	if err := cfg.EnsureCBNDir(); err != nil {
		t.Fatal(err)
	}
	a, err := New(cfg, Options{Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	return a
}
