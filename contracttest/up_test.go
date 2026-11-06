package contracttest

import (
	"os"
	"strings"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/connect"
)

func TestSecondUpIsRefused(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newStack(t)
	before, err := os.ReadFile(s.cfg.Path("control.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := connect.CheckPorts(s.cfg); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second start while running: %v", err)
	}
	if err := os.Remove(s.cfg.Path("running")); err != nil {
		t.Fatal(err)
	}
	if err := connect.CheckPorts(s.cfg); err == nil || !strings.Contains(err.Error(), "another program") {
		t.Fatalf("start with a busy port and no run marker: %v", err)
	}
	after, _ := os.ReadFile(s.cfg.Path("control.json"))
	if string(after) != string(before) {
		t.Fatal("checking the ports changed control.json")
	}
}
