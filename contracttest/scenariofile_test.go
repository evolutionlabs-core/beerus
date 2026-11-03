package contracttest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func (s *Stack) runScenarioFile(body []byte, contentType, query string) (int, map[string]any) {
	s.t.Helper()
	req, _ := http.NewRequest("POST", s.cfg.ControlURL()+"/scenarios/run"+query, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+s.ctlToken)
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return resp.StatusCode, out
}

func errorMessage(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	return fmt.Sprint(e["message"])
}

func TestScenarioFiles(t *testing.T) {
	s := newStack(t)
	files, err := filepath.Glob("../examples/scenarios/*.yaml")
	if err != nil || len(files) < 2 {
		t.Fatalf("example files: %v %v", files, err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		status, out := s.runScenarioFile(raw, "application/yaml", "")
		if status != http.StatusOK {
			t.Fatalf("%s: HTTP %d %s", f, status, errorMessage(out))
		}
		steps, _ := out["steps"].([]any)
		if len(steps) == 0 || out["scenario"] == "" {
			t.Fatalf("%s: %v", f, out)
		}
	}
	s.syncNow()

	if err := os.MkdirAll(s.cfg.Path("scenarios"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.cfg.Path("scenarios", "one.yaml"), []byte("name: one\nsteps:\n  - advance: 1h\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, out := s.runScenarioFile([]byte(`{"path":"one.yaml"}`), "application/json", "")
	if status != http.StatusOK || out["scenario"] != "one" {
		t.Fatalf("by path: %d %v", status, out)
	}
	status, out = s.runScenarioFile(nil, "application/json", "?path=.beerus/scenarios/one.yaml")
	if status != http.StatusOK {
		t.Fatalf("by query path: %d %v", status, out)
	}
	for _, p := range []string{"../one.yaml", "/etc/hosts", "missing.yaml", "one.txt"} {
		body, _ := json.Marshal(map[string]string{"path": p})
		if status, _ := s.runScenarioFile(body, "application/json", ""); status < 400 {
			t.Fatalf("path %q was accepted", p)
		}
	}

	bad := []struct{ name, file, want string }{
		{"empty", "", "empty"},
		{"no name", "steps:\n  - advance: 1h\n", "name is required"},
		{"no steps", "name: x\n", "steps is empty"},
		{"unknown field", "name: x\nsteps:\n  - advance: 1h\n    speed: 3\n", `line 4: unknown field "speed"`},
		{"not yaml", "name: [x\n", "not valid YAML"},
		{"no action", "name: x\nsteps:\n  - count: 2\n", "step 1: use one of"},
		{"two actions", "name: x\nsteps:\n  - advance: 1h\n    suspend: bank-a\n", "step 1: use only one"},
		{"unknown scenario", "name: x\nsteps:\n  - advance: 1h\n  - run: nope\n", `step 2: unknown scenario "nope"`},
		{"bad duration", "name: x\nsteps:\n  - advance: soon\n", "step 1: advance must look like"},
		{"unknown bank", "name: x\nsteps:\n  - suspend: bank-z\n", `unknown bank "bank-z"`},
		{"unknown persona", "name: x\nsteps:\n  - run: late_reversal\n    persona: nobody\n", `unknown persona "nobody"`},
		{"bad amount", "name: x\nsteps:\n  - transfer:\n      from: a\n      to: b\n      amount: lots\n", "step 1: amount"},
		{"missing transfer field", "name: x\nsteps:\n  - transfer:\n      from: a\n", "transfer needs from, to and amount"},
		{"stray field", "name: x\nsteps:\n  - advance: 1h\n    account: a\n", "only go with run"},
	}
	for _, c := range bad {
		status, out := s.runScenarioFile([]byte(c.file), "application/yaml", "")
		if status != http.StatusBadRequest || !strings.Contains(errorMessage(out), c.want) {
			t.Errorf("%s: HTTP %d %q, want %q", c.name, status, errorMessage(out), c.want)
		}
	}

	status, out = s.runScenarioFile([]byte("name: x\nsteps:\n  - advance: 1h\n  - run: late_reversal\n    account: nobody@bank-a\n"), "application/yaml", "")
	if status != http.StatusBadRequest || !strings.Contains(errorMessage(out), "step 2") || !strings.Contains(errorMessage(out), "Steps 1 to 1 already ran") {
		t.Errorf("failing step: HTTP %d %q", status, errorMessage(out))
	}
}

func TestPersonaList(t *testing.T) {
	s := newStack(t)
	data, _ := s.ctl("GET", "/personas", nil)["data"].([]any)
	if len(data) < 18 {
		t.Fatalf("%d personas", len(data))
	}
	for _, id := range []string{"adaeze", "tunde", "ngozi", "chidi"} {
		s.persona(id)
	}
}
