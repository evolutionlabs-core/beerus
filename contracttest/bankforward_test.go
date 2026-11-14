package contracttest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/config"
)

const stoppedBank = "beerus: Bank A is not running or cannot be reached. Run beerus get banks to see its state, then try again."

type cliResult struct {
	out, errOut string
	code        int
}

func TestCustomerCallsReachTheBankProcess(t *testing.T) {
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
	local := fmt.Sprintf("database_url: postgres:///postgres\nconnect:\n  host: connect.localhost\n  partner_port: %d\n  widget_port: %d\n  control_port: %d\n  health_port: %d\n  nibss_port: %d\n  sync_interval: 1m\n  refresh_min_interval: 1m\n", ports[0], ports[1], ports[2], ports[3], ports[4])
	if err := os.WriteFile(filepath.Join(dir, "local.yaml"), []byte(local), 0o600); err != nil {
		t.Fatal(err)
	}
	c := newCLIAt(t, dir)
	startUp(t, c, "")
	c.ok("bank", "create", "--name", "Bank A")
	customer, _ := c.capture(`Created customer (cus_\w+), Ada Obi, at bank-a`, "customer", "create", "--bank", "bank-a", "--name", "Ada Obi", "--phone", "08030000000", "--dob", "1990-04-12")
	account, _ := c.capture(`Opened savings account (\d{10}) for Ada Obi`, "account", "open", "--bank", "bank-a", "--customer", customer, "--type", "savings")
	steps := []struct {
		args []string
		want string
	}{
		{[]string{"deposit", account, "--bank", "bank-a", "--amount", "5000"}, fmt.Sprintf("Deposited ₦5,000.00 into account %s at bank-a. Balance ₦5,000.00.\n", account)},
		{[]string{"withdraw", account, "--bank", "bank-a", "--amount", "1200"}, fmt.Sprintf("Withdrew ₦1,200.00 from account %s at bank-a. Balance ₦3,800.00.\n", account)},
	}
	for _, s := range steps {
		if got := c.ok(s.args...); got != s.want {
			t.Fatalf("beerus %v printed %q, want %q", s.args, got, s.want)
		}
	}
	if got := c.fails("withdraw", account, "--bank", "bank-a", "--amount", "100000"); got != "beerus: Account "+account+" holds ₦3,800.00 and ₦100,000.00 was asked for; withdraw ₦3,800.00 or less." {
		t.Fatalf("overdraw: %q", got)
	}
	statement := strings.Join(strings.Fields(c.ok("statement", account, "--bank", "bank-a")), " ")
	if !strings.Contains(statement, "₦5,000.00 ₦5,000.00 CASH DEPOSIT") || !strings.Contains(statement, "−₦1,200.00 ₦3,800.00 CASH WITHDRAWAL") || !strings.HasSuffix(statement, "2 entries on this page.") {
		t.Fatalf("statement: %q", statement)
	}

	pid := processesIn(t, dir)["bank-a"]
	if pid == 0 {
		t.Fatal("bank-a has no process")
	}
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		t.Fatalf("stop bank-a: %v", err)
	}
	var once sync.Once
	resume := func() { once.Do(func() { _ = syscall.Kill(pid, syscall.SIGCONT) }) }
	t.Cleanup(resume)
	calls := [][]string{
		{"accounts", "--bank", "bank-a"},
		{"customers", "--bank", "bank-a"},
		{"statement", account, "--bank", "bank-a"},
		{"deposit", "0000000000", "--bank", "bank-a", "--amount", "1"},
		{"customer", "create", "--bank", "bank-a", "--name", "Bola Ade", "--phone", "08030000001", "--dob", "1991-01-01"},
	}
	results := make([]cliResult, len(calls))
	var wg sync.WaitGroup
	for i, args := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, errOut, code := c.run(args...)
			results[i] = cliResult{out, errOut, code}
		}()
	}
	wg.Wait()
	resume()
	for i, r := range results {
		if r.code != 1 || r.out != "" || r.errOut != stoppedBank {
			t.Errorf("beerus %v while the bank was stopped: exit %d, output %q, error %q", calls[i], r.code, r.out, r.errOut)
		}
	}
	waitFor(t, "the bank to answer again", func() bool {
		got, _, code := c.run("accounts", "--bank", "bank-a")
		return code == 0 && strings.Contains(got, "₦3,800.00")
	})
	if got := c.ok("customers", "--bank", "bank-a"); !strings.HasSuffix(got, "1 customer on this page.\n") {
		t.Errorf("customers after the stop: %q", got)
	}
	c.ok("bank", "close", "bank-a", "--money", "leave", "--yes")
	if got := c.fails("statement", account, "--bank", "bank-a"); got != "beerus: Bank A is closed." {
		t.Errorf("statement of a closed bank: %q", got)
	}
}
