package participant

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

func TestStatusWithoutTheSettlementCountReportsAProblem(t *testing.T) {
	dir := t.TempDir()
	p := &Participant{dir: dir, log: slog.New(slog.DiscardHandler), clock: clock.New(nil)}
	w := httptest.NewRecorder()
	p.status(w, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	problems, _ := got["problems"].([]any)
	_, counted := got["posted_sessions"]
	prefix := "NIBSS could not check its settlements: "
	if w.Code != http.StatusOK || counted || len(problems) != 1 || !strings.HasPrefix(problems[0].(string), prefix) || !strings.Contains(problems[0].(string), config.NIBSSFilePath(dir)) {
		t.Errorf("%d %v, want 200 with one problem starting %q naming %s and no posted_sessions", w.Code, got, prefix, config.NIBSSFilePath(dir))
	}
}
