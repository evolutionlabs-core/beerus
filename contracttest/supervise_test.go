package contracttest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
)

var participantProcess = regexp.MustCompile(`^\s*(\d+) .* run --participant (\S+) `)

func processesIn(t *testing.T, dir string) map[string]int {
	t.Helper()
	out, err := exec.Command("ps", "-axww", "-o", "pid=,command=").Output()
	if err != nil {
		t.Fatalf("list processes: %v", err)
	}
	found := map[string]int{}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, " --dir "+dir+" ") && !strings.HasSuffix(line, " --dir "+dir) {
			continue
		}
		m := participantProcess.FindStringSubmatch(line + " ")
		if m == nil {
			t.Fatalf("unexpected process using %s: %q", dir, line)
		}
		pid, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("read pid in %q: %v", line, err)
		}
		found[m[2]] = pid
	}
	return found
}

func TestSupervisorRestartsABankAndStopsEverything(t *testing.T) {
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
	c.ok("init")
	up := exec.Command(c.bin, "up")
	up.Env = c.env()
	out, errOut := &lockedBuffer{}, &lockedBuffer{}
	up.Stdout, up.Stderr = out, errOut
	if err := up.Start(); err != nil {
		t.Fatalf("start beerus up: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- up.Wait() }()
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = up.Process.Kill()
			<-exited
		}
		if got, errOut, code := c.run("reset", "beerus"); code != 0 || !strings.HasPrefix(got, "Dropped databases:\n") {
			t.Errorf("reset: exit %d, output %q, error %q", code, got, errOut)
		}
	})
	waitFor(t, "beerus up to print that it is running", func() bool {
		select {
		case err := <-exited:
			t.Fatalf("beerus up exited: %v\n%s\n%s", err, out.String(), errOut.String())
		default:
		}
		return strings.HasSuffix(out.String(), "\nPress Ctrl+C to stop.\n")
	})

	c.ok("bank", "create", "--name", "Bank A")
	customer, _ := c.capture(`Created customer (cus_\w+), Ada Obi, at bank-a`, "customer", "create", "--bank", "bank-a", "--name", "Ada Obi", "--phone", "08030000000", "--dob", "1990-04-12")
	account, _ := c.capture(`Opened savings account (\d{10}) for Ada Obi`, "account", "open", "--bank", "bank-a", "--customer", customer, "--type", "savings")
	if got := c.ok("deposit", account, "--bank", "bank-a", "--amount", "5,000.50"); !strings.Contains(got, "Balance ₦5,000.50.") {
		t.Fatalf("deposit: %q", got)
	}
	waitFor(t, "the core and bank-a processes", func() bool {
		found := processesIn(t, dir)
		return found["core"] != 0 && found["bank-a"] != 0
	})
	accounts, statement := c.ok("accounts", "--bank", "bank-a"), c.ok("statement", account, "--bank", "bank-a")
	if !strings.Contains(accounts, "5,000.50") || !strings.Contains(statement, "CASH DEPOSIT") {
		t.Fatalf("before the kill: accounts %q, statement %q", accounts, statement)
	}

	old := processesIn(t, dir)["bank-a"]
	if err := syscall.Kill(old, syscall.SIGKILL); err != nil {
		t.Fatalf("kill bank-a: %v", err)
	}
	waitFor(t, "bank-a to come back", func() bool {
		pid := processesIn(t, dir)["bank-a"]
		return pid != 0 && pid != old
	})
	waitFor(t, "the balance and statement to match after the restart", func() bool {
		a, aErr, aCode := c.run("accounts", "--bank", "bank-a")
		s, sErr, sCode := c.run("statement", account, "--bank", "bank-a")
		return aCode == 0 && sCode == 0 && aErr == "" && sErr == "" && a == accounts && s == statement
	})

	if err := up.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("interrupt beerus up: %v", err)
	}
	select {
	case err := <-exited:
		stopped = true
		if err != nil {
			t.Fatalf("beerus up after Ctrl+C: %v\n%s", err, errOut.String())
		}
	case <-time.After(60 * time.Second):
		t.Fatalf("beerus up did not stop\n%s", errOut.String())
	}
	if left := processesIn(t, dir); len(left) != 0 {
		t.Fatalf("participants left after up exited: %v", left)
	}
}
