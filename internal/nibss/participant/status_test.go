package participant

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

func TestStatusWithoutTheSettlementCountReportsAProblem(t *testing.T) {
	dir := t.TempDir()
	p := &Participant{dir: dir, log: slog.New(slog.DiscardHandler), clock: clock.New()}
	w := httptest.NewRecorder()
	p.status(w, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	problems, _ := got["problems"].([]any)
	_, counted := got["posted_sessions"]
	_, dated := got["next_cutoff_at"]
	prefix := "NIBSS could not check its settlements: "
	if w.Code != http.StatusOK || counted || dated || len(problems) != 1 || !strings.HasPrefix(problems[0].(string), prefix) || !strings.Contains(problems[0].(string), config.NIBSSFilePath(dir)) {
		t.Errorf("%d %v, want 200 with one problem starting %q naming %s and no posted_sessions or next_cutoff_at", w.Code, got, prefix, config.NIBSSFilePath(dir))
	}
}

func TestNextCutoffFollowsTheNIBSSFile(t *testing.T) {
	at := func(day string, hh int) time.Time {
		d, err := clock.ParseDate(day)
		if err != nil {
			t.Fatal(err)
		}
		return d.Start().Add(time.Duration(hh) * time.Hour)
	}
	file := config.NIBSSFile{Sessions: 4, HolidaysAdd: []string{"2026-11-09"}, HolidaysRemove: []string{"2026-10-01"}}
	rows := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{"a weekday evening", at("2026-11-04", 20), at("2026-11-05", 0)},
		{"just after a session", at("2026-11-04", 12), at("2026-11-04", 18)},
		{"friday night runs into the weekend", at("2026-11-06", 20), at("2026-11-07", 0)},
		{"saturday waits for the added holiday", at("2026-11-07", 9), at("2026-11-10", 6)},
		{"christmas day waits for monday", at("2026-12-25", 0), at("2026-12-28", 6)},
		{"a removed holiday is a business day", at("2026-09-30", 20), at("2026-10-01", 0)},
	}
	sched, err := scheduleOf(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if got := sched.Next(r.now); !got.Equal(r.want) {
			t.Errorf("%s: next cutoff after %s is %s, want %s", r.name, r.now.Format(time.RFC3339), got.Format(time.RFC3339), r.want.Format(time.RFC3339))
		}
	}
	if _, err := scheduleOf(config.NIBSSFile{Sessions: 4, HolidaysAdd: []string{"soon"}}); err == nil || err.Error() != `holidays.add: "soon" is not a date (use YYYY-MM-DD)` {
		t.Errorf("a bad holiday: %v", err)
	}
}
