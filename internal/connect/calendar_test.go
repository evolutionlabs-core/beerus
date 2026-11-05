package connect

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

func calendarWarning(t *testing.T, state string) bool {
	t.Helper()
	cfg := initTestConfig(t, clock.Date{Y: 2026, M: 11, D: 3})
	if state != "" {
		if err := os.WriteFile(cfg.Path("stub-state.json"), []byte(state), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	app, err := New(cfg, Options{WorldOnly: true, Logger: slog.New(slog.NewTextHandler(&buf, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if app.st.Calendar != clock.CalendarVersion() {
		t.Errorf("state calendar %q, want %q", app.st.Calendar, clock.CalendarVersion())
	}
	return strings.Contains(buf.String(), "holiday calendar changed")
}

func TestCalendarVersionWarning(t *testing.T) {
	if calendarWarning(t, "") {
		t.Error("a new setup should not warn")
	}
	if !calendarWarning(t, `{"world_id":"wld_old"}`) {
		t.Error("a state from before the calendar was recorded should warn")
	}
	if !calendarWarning(t, `{"world_id":"wld_old","calendar":"ng-0"}`) {
		t.Error("a state with another calendar version should warn")
	}
	if calendarWarning(t, fmt.Sprintf(`{"world_id":"wld_old","calendar":%q}`, clock.CalendarVersion())) {
		t.Error("a state with the current calendar should not warn")
	}
}
