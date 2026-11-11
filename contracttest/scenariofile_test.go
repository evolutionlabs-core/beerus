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
	if err != nil || len(files) == 0 {
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

	from, to := s.persona("tunde").Accounts[0], s.persona("chidi").Accounts[0]
	pay := "name: pay\nsteps:\n  - transfer:\n      from_bank: bank-a\n      from: \"" + from + "\"\n      to_bank: bank-b\n      to: \"" + to + "\"\n      amount: \"5000\"\n"
	status, out := s.runScenarioFile([]byte(pay), "application/yaml", "")
	if steps, _ := out["steps"].([]any); status != http.StatusOK || len(steps) != 1 || !strings.Contains(fmt.Sprint(steps[0]), "₦5,000.00 success") {
		t.Fatalf("a transfer step: HTTP %d %v", status, out)
	}
	if got := s.balances(s.persona("chidi"))[to]; got != 8_500_000+500_000 {
		t.Fatalf("chidi has %d after the transfer step", got)
	}

	if err := os.MkdirAll(s.cfg.Path("scenarios"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.cfg.Path("scenarios", "one.yaml"), []byte("name: one\nsteps:\n  - suspend: bank-a\n  - resume: bank-a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, out = s.runScenarioFile([]byte(`{"path":"one.yaml"}`), "application/json", "")
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
		{"no name", "steps:\n  - resume: bank-a\n", "name is required"},
		{"no steps", "name: x\n", "steps is empty"},
		{"unknown field", "name: x\nsteps:\n  - resume: bank-a\n    speed: 3\n", `line 4: unknown field "speed"`},
		{"not yaml", "name: [x\n", "not valid YAML"},
		{"no action", "name: x\nsteps:\n  - connection: con_1\n", "step 1: use one of"},
		{"two actions", "name: x\nsteps:\n  - resume: bank-a\n    suspend: bank-a\n", "step 1: use only one"},
		{"unknown scenario", "name: x\nsteps:\n  - resume: bank-a\n  - run: nope\n", `step 2: unknown scenario "nope"`},
		{"advance step", "name: x\nsteps:\n  - advance: 1h\n", `line 3: unknown field "advance"`},
		{"unknown bank", "name: x\nsteps:\n  - suspend: bank-z\n", `unknown bank "bank-z"`},
		{"unknown from bank", "name: x\nsteps:\n  - transfer:\n      from_bank: bank-z\n      from: a\n      to: b\n      amount: \"5\"\n", `step 1: unknown bank "bank-z"`},
		{"bad amount", "name: x\nsteps:\n  - transfer:\n      from_bank: bank-a\n      from: a\n      to: b\n      amount: lots\n", "step 1: amount"},
		{"missing transfer field", "name: x\nsteps:\n  - transfer:\n      from: a\n", "transfer needs from_bank, from, to and amount"},
		{"stray field", "name: x\nsteps:\n  - resume: bank-a\n    connection: con_1\n", "connection only goes with run"},
	}
	for _, c := range bad {
		status, out := s.runScenarioFile([]byte(c.file), "application/yaml", "")
		if status != http.StatusBadRequest || !strings.Contains(errorMessage(out), c.want) {
			t.Errorf("%s: HTTP %d %q, want %q", c.name, status, errorMessage(out), c.want)
		}
	}

	status, out = s.runScenarioFile([]byte("name: x\nsteps:\n  - resume: bank-a\n  - transfer:\n      from_bank: bank-a\n      from: \"0000000000\"\n      to: \""+from+"\"\n      amount: \"5\"\n"), "application/yaml", "")
	if status != http.StatusBadRequest || !strings.HasPrefix(errorMessage(out), "step 2 (transfer) failed: ") || !strings.HasSuffix(errorMessage(out), " Step 1 already ran.") {
		t.Errorf("failing step: HTTP %d %q", status, errorMessage(out))
	}

	s.ctl("POST", "/banks/bank-b/close", map[string]any{"money": "leave"})
	for _, step := range []string{"suspend", "resume"} {
		status, out = s.runScenarioFile([]byte("name: x\nsteps:\n  - "+step+": bank-b\n"), "application/yaml", "")
		if status != http.StatusBadRequest || !strings.Contains(errorMessage(out), "Bank B is closed; a closed bank cannot be suspended or resumed") {
			t.Errorf("%s a closed bank: HTTP %d %q", step, status, errorMessage(out))
		}
	}
}
