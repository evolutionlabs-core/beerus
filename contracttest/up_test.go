package contracttest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/config"
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

func TestFreshUpHasNoBanks(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a full stack; run without -short")
	}
	if u := os.Getenv("BEERUS_TEST_DATABASE_URL"); u != "" {
		t.Setenv(config.DatabaseURLEnv, u)
	}
	dir := filepath.Join(t.TempDir(), "beerus")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ports := stackPorts(t, 1)
	local := fmt.Sprintf("database_url: postgres:///postgres\nconnect:\n  host: connect.localhost\n  partner_port: %d\n  widget_port: %d\n  control_port: %d\n  health_port: %d\n  sync_interval: 1m\n  refresh_min_interval: 1m\n", ports[0], ports[1], ports[2], ports[3])
	if err := os.WriteFile(filepath.Join(dir, "local.yaml"), []byte(local), 0o600); err != nil {
		t.Fatal(err)
	}
	c := newCLIAt(t, dir)
	out, errOut := startUp(t, c, "")
	if text := out.String(); strings.Contains(text, "online banking") || !strings.Contains(text, "\nNo banks yet. Add the first one with beerus bank create --name NAME.\n") || strings.Contains(errOut.String(), "level=ERROR") {
		t.Fatalf("up printed %q and logged %q", text, errOut.String())
	}
	if got := c.ok("get", "banks"); got != "ID  NAME  PHASE  READY  GEN  MESSAGE\n" {
		t.Errorf("get banks: %q", got)
	}
	if got := c.ok("nip", "settlement"); !strings.Contains(got, `"sessions": 0`) {
		t.Errorf("nip settlement: %q", got)
	}
	const noBanks = "beerus: there are no banks yet. Add one first with beerus bank create --name NAME"
	if got := c.fails("use", "bank-a"); got != noBanks {
		t.Errorf("use: %q", got)
	}
	if got := c.fails("customer", "create", "--name", "Ada Obi", "--phone", "08030000000", "--dob", "1990-04-12"); got != noBanks {
		t.Errorf("customer create: %q", got)
	}
	if got := c.fails("customer", "create", "--bank", "bank-a", "--name", "Ada Obi", "--phone", "08030000000", "--dob", "1990-04-12"); got != noBanks {
		t.Errorf("customer create at an unknown bank: %q", got)
	}
	if got := c.fails("fault", "down", "--bank", "bank-a"); got != noBanks {
		t.Errorf("fault down at an unknown bank: %q", got)
	}
	c.ok("bank", "create", "--name", "Bank A")
	if got := c.fails("fault", "down", "--bank", "bank-z"); got != "beerus: No such bank." {
		t.Errorf("fault down at an unknown bank: %q", got)
	}
	if got := c.ok("get", "banks"); !strings.Contains(got, "bank-a  Bank A  active  yes") {
		t.Errorf("get banks after the first bank: %q", got)
	}
	if got := c.ok("info"); !strings.Contains(got, "Bank A online banking") {
		t.Errorf("info after the first bank: %q", got)
	}
	if got := c.ok("customer", "create", "--bank", "bank-a", "--name", "Ada Obi", "--phone", "08030000000", "--dob", "1990-04-12"); !strings.HasPrefix(got, "Created customer ") {
		t.Errorf("customer create at the first bank: %q", got)
	}
}
