package contracttest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

type cli struct {
	t    *testing.T
	bin  string
	dir  string
	home string
	in   string
}

func newCLI(t *testing.T, s *Stack) *cli {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	bin := filepath.Join(t.TempDir(), "beerus")
	build := exec.Command("go", "build", "-o", bin, "github.com/evolutionlabs-core/beerus/cmd/beerus")
	build.Dir = filepath.Dir(file)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build beerus: %v\n%s", err, out)
	}
	return &cli{t: t, bin: bin, dir: s.cfg.Dir, home: t.TempDir()}
}

func (c *cli) env(extra ...string) []string {
	env := []string{"PATH=" + filepath.Dir(c.bin) + ":" + os.Getenv("PATH"), "HOME=" + c.home, "TMPDIR=" + os.TempDir(), "BEERUS_DIR=" + c.dir, "TERM=xterm", "LANG=en_US.UTF-8", "BASH_SILENCE_DEPRECATION_WARNING=1"}
	return append(env, extra...)
}

func (c *cli) run(args ...string) (string, string, int) {
	c.t.Helper()
	cmd := exec.Command(c.bin, args...)
	cmd.Env = c.env()
	cmd.Stdin = strings.NewReader(c.in)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	code := 0
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		code = exit.ExitCode()
	case err != nil:
		c.t.Fatalf("run beerus %v: %v", args, err)
	}
	return out.String(), strings.TrimSpace(errOut.String()), code
}

func (c *cli) ok(args ...string) string {
	c.t.Helper()
	out, errOut, code := c.run(args...)
	if code != 0 {
		c.t.Fatalf("beerus %v exited %d: %s", args, code, errOut)
	}
	return out
}

func (c *cli) fails(args ...string) string {
	c.t.Helper()
	out, errOut, code := c.run(args...)
	if code != 1 || out != "" {
		c.t.Fatalf("beerus %v: want exit 1 and no output, got exit %d, output %q, error %q", args, code, out, errOut)
	}
	return errOut
}

func (c *cli) json(args ...string) map[string]any {
	c.t.Helper()
	out := map[string]any{}
	if err := json.Unmarshal([]byte(c.ok(append(args, "--json")...)), &out); err != nil {
		c.t.Fatalf("beerus %v --json is not JSON: %v", args, err)
	}
	return out
}

func (c *cli) capture(pattern string, args ...string) (string, string) {
	c.t.Helper()
	out := c.ok(args...)
	m := regexp.MustCompile(pattern).FindStringSubmatch(out)
	if m == nil {
		c.t.Fatalf("beerus %v: no match for %s in %q", args, pattern, out)
	}
	return m[1], out
}

func TestBankCLI(t *testing.T) {
	s := newBankStack(t)
	c := newCLI(t, s)
	const noBank = "beerus: No bank selected. Run beerus use <bank> or add --bank."
	for _, args := range [][]string{{"customers"}, {"accounts"}, {"deposit", "0123456789", "--amount", "5"}, {"transfer", "--from", "1", "--to", "2", "--amount", "5"}, {"fault", "down"}} {
		if got := c.fails(args...); got != noBank {
			t.Errorf("beerus %v: %q", args, got)
		}
	}

	adaID, out := c.capture(`Created customer (cus_\w+), Adaeze Okafor, at bank-a\. BVN (\d+)\.`, "customer", "create", "--bank", "bank-a", "--name", "Adaeze Okafor", "--phone", "08031234567", "--dob", "1988-03-14")
	if !strings.Contains(out, "Business time: Thu 15 Jan 2026 08:00 WAT") {
		t.Errorf("customer create has no business time: %q", out)
	}
	bvn := regexp.MustCompile(`BVN (\d+)`).FindStringSubmatch(out)[1]
	chidiID, out := c.capture(`Created customer (cus_\w+), Chidi Okafor, at bank-b`, "customer", "create", "--bank", "bank-b", "--name", "Chidi Okafor", "--phone", "08034567890", "--dob", "1995-05-30")
	if strings.Contains(out, bvn) {
		t.Errorf("two people share a BVN: %q", out)
	}
	again := c.json("customer", "create", "--bank", "bank-b", "--name", "Adaeze Okafor", "--phone", "08031234567", "--dob", "1988-03-14")
	if again["bvn"] != bvn {
		t.Errorf("the same person got a second BVN: %v and %v", again["bvn"], bvn)
	}
	if out := c.ok("customers", "--bank", "bank-a"); !strings.Contains(out, "Adaeze Okafor") || !strings.Contains(out, "1 customer on this page.") {
		t.Errorf("customers: %q", out)
	}
	if out := c.ok("customers", "--bank", "bank-b"); !strings.Contains(out, "2 customers on this page.") {
		t.Errorf("customers at bank-b: %q", out)
	}
	page := c.json("customers", "--bank", "bank-b", "--limit", "1")
	if next, _ := page["next_cursor"].(string); next == "" || len(page["data"].([]any)) != 1 {
		t.Fatalf("customers --limit 1: %v", page)
	}
	if out := c.ok("customers", "--bank", "bank-b", "--limit", "1"); !strings.Contains(out, "More results. Run again with --cursor "+page["next_cursor"].(string)+" to see the next page.") {
		t.Errorf("no next page hint: %q", out)
	}

	a1, out := c.capture(`Opened current account (\d{10}) for Adaeze Okafor \(`+adaID+`\) at bank-a\. Balance ₦0\.00\.`, "account", "open", "--bank", "bank-a", "--customer", adaID, "--type", "current")
	a2, _ := c.capture(`Opened savings account (\d{10})`, "account", "open", "--bank", "bank-a", "--customer", adaID, "--type", "savings")
	b1, _ := c.capture(`Opened current account (\d{10})`, "account", "open", "--bank", "bank-b", "--customer", chidiID, "--type", "current")
	if out := c.ok("accounts", "--bank", "bank-a", "--customer", adaID); !strings.Contains(out, a1) || !strings.Contains(out, a2) || !strings.Contains(out, "2 accounts on this page.") {
		t.Errorf("accounts: %q", out)
	}
	if out := c.ok("accounts", "--bank", "bank-b", "--customer", chidiID); !strings.Contains(out, "1 account on this page.") {
		t.Errorf("accounts at bank-b: %q", out)
	}

	if got := c.ok("deposit", a1, "--bank", "bank-a", "--amount", "5,000.50"); got != fmt.Sprintf("Deposited ₦5,000.50 into account %s at bank-a. Balance ₦5,000.50.\nBusiness time: Thu 15 Jan 2026 08:00 WAT\n", a1) {
		t.Errorf("deposit: %q", got)
	}
	if got := c.ok("withdraw", a1, "--bank", "bank-a", "--amount", "0.50"); got != fmt.Sprintf("Withdrew ₦0.50 from account %s at bank-a. Balance ₦5,000.00.\nBusiness time: Thu 15 Jan 2026 08:00 WAT\n", a1) {
		t.Errorf("withdraw: %q", got)
	}
	if got := c.ok("transfer", "--from-bank", "bank-a", "--from", a1, "--to", a2, "--amount", "1000"); got != fmt.Sprintf("Moved ₦1,000.00 from %s to %s, Adaeze Okafor, at 08:00 WAT. Balance ₦4,000.00.\n", a1, a2) {
		t.Errorf("same-bank transfer: %q", got)
	}

	sent := c.json("transfer", "--bank", "bank-a", "--from", a1, "--to", b1, "--to-bank", "bank-b", "--amount", "1000", "--narration", "rent")
	total := sent["amount_minor"].(float64) + sent["fee_minor"].(float64) + sent["vat_minor"].(float64) + sent["stamp_duty_minor"].(float64)
	if sent["status"] != "success" || sent["response_code"] != "00" || sent["total_debit_minor"] != total || sent["sender_balance_after_minor"] != 400000-total || sent["fee_minor"].(float64) <= 0 {
		t.Errorf("nip transfer: %v", sent)
	}
	sid, out := c.capture(`Session (\d+): success at 08:00 WAT`, "transfer", "--from-bank", "bank-a", "--from", a1, "--to", b1, "--to-bank", "bank-b", "--amount", "1000.50")
	if !strings.HasPrefix(out, "Sending ₦1,000.50 to Chidi Okafor at Bank B.\n") || !strings.Contains(out, "Total debit ₦") {
		t.Errorf("nip transfer text: %q", out)
	}
	if got := c.ok("nip", "status", sid); !strings.HasPrefix(got, "Session "+sid+": success at 08:00 WAT\nAmount ₦1,000.50 from "+a1+" (bank code 999001) to "+b1+" (bank code 999002), started at 08:00 WAT.\n") {
		t.Errorf("nip status: %q", got)
	}
	if out := c.ok("statement", a1, "--bank", "bank-a", "--from", "2026-01-15", "--to", "2026-01-15"); !strings.Contains(out, "9 entries on this page.") || !strings.Contains(out, sid) || !strings.Contains(out, "−₦1,000.50") {
		t.Errorf("statement: %q", out)
	}
	if lines := c.json("statement", a1, "--bank", "bank-a")["data"].([]any); len(lines) != 9 {
		t.Errorf("statement --json has %d lines, want 9", len(lines))
	}

	if out := c.ok("fault", "timeout", "--bank", "bank-b", "--outcome", "credited"); !regexp.MustCompile(`^Fault flt_\w+ on bank-b: the next transfer times out \(outcome credited\)\.\n`).MatchString(out) {
		t.Errorf("fault timeout: %q", out)
	}
	if out := c.ok("fault", "list"); !strings.Contains(out, "bank-b") || !strings.Contains(out, "the next transfer times out (outcome credited)") {
		t.Errorf("fault list: %q", out)
	}
	psid, out := c.capture(`Session (\d+): pending\. Beerus will check with NIBSS at \d\d:\d\d WAT\. Run beerus nip status \d+\.`, "transfer", "--from-bank", "bank-a", "--from", a1, "--to", b1, "--to-bank", "bank-b", "--amount", "10")
	if got := c.ok("nip", "status", psid); !strings.HasPrefix(got, "Session "+psid+": pending. Beerus will check with NIBSS at ") {
		t.Errorf("pending status: %q", got)
	}
	s.advance("1h")
	if got := c.ok("nip", "status", psid); !strings.HasPrefix(got, "Session "+psid+": success at 09:00 WAT\n") {
		t.Errorf("resolved status: %q", got)
	}
	if got := c.ok("fault", "clear"); got != "No faults to clear.\nBusiness time: Thu 15 Jan 2026 09:00 WAT\n" {
		t.Errorf("fault clear with none: %q", got)
	}

	c.ok("fault", "down", "--bank", "bank-b", "--for", "30m")
	if got := c.fails("transfer", "--from-bank", "bank-a", "--from", a1, "--to", b1, "--to-bank", "bank-b", "--amount", "10"); got != "beerus: Bank B is not available right now, so nothing was debited." {
		t.Errorf("transfer to a bank that is down: %q", got)
	}
	if got := c.ok("fault", "clear", "--bank", "bank-b"); got != "Cleared 1 fault.\nBusiness time: Thu 15 Jan 2026 09:00 WAT\n" {
		t.Errorf("fault clear: %q", got)
	}

	if got := c.json("nip", "settle-now"); got["settled_sessions"] == nil || !strings.HasPrefix(fmt.Sprint(got["business_time"]), "2026-01-15T09:00:") {
		t.Errorf("settle-now --json: %v", got)
	}
	if got := c.ok("nip", "settle-now"); !strings.HasPrefix(got, "No sessions to settle at 09:00 WAT.") {
		t.Errorf("settle-now again: %q", got)
	}
	for _, args := range [][]string{{"accounts", "--bank", "bank-a"}, {"fault", "list"}, {"nip", "status", sid}, {"customers", "--bank", "bank-a"}} {
		if out := c.json(args...); out["data"] == nil && out["session_id"] == nil {
			t.Errorf("beerus %v --json: %v", args, out)
		}
	}

	c.in = "\n"
	if got := c.fails("customer", "create", "--bank", "bank-a", "--name", "Tunde Motors", "--phone", "08032345678", "--dob", "1979-07-02", "--username", "tunde", "--password-stdin"); got != "beerus: --password-stdin read an empty password" {
		t.Errorf("empty password: %q", got)
	}
	c.in = "a-long-test-password\n"
	if out := c.ok("customer", "create", "--bank", "bank-a", "--name", "Tunde Motors", "--phone", "08032345678", "--dob", "1979-07-02", "--username", "tunde", "--password-stdin"); !strings.Contains(out, "Tunde Motors can sign in to online banking as tunde.\n") {
		t.Errorf("customer with a username: %q", out)
	}
	c.in = ""

	cases := []struct {
		args []string
		want string
	}{
		{[]string{"deposit", a1, "--bank", "bank-a"}, "beerus: --amount is required; usage: beerus deposit <nuban> --amount NAIRA"},
		{[]string{"deposit", a1, "--bank", "bank-a", "--amount", "ten"}, `beerus: Amount "ten" must look like 25000 or 25000.50 (naira).`},
		{[]string{"deposit", a1, "--bank", "bank-a", "--amount", "1.234"}, `beerus: Amount "1.234" must look like 25000 or 25000.50 (naira).`},
		{[]string{"deposit", a1, "--bank", "bank-a", "--amount", "0"}, `beerus: Amount "0" must be a positive naira amount.`},
		{[]string{"deposit", "--amount", "5"}, `beerus: "--amount" should be a value, not a flag; usage: beerus deposit <nuban> --amount NAIRA`},
		{[]string{"deposit"}, "beerus: usage: beerus deposit <nuban> --amount NAIRA"},
		{[]string{"deposit", "0000000000", "--bank", "bank-a", "--amount", "5"}, "beerus: No such account."},
		{[]string{"deposit", a1, "--bank", "bank-z", "--amount", "5"}, "beerus: No such bank."},
		{[]string{"withdraw", a2, "--bank", "bank-a", "--amount", "999999"}, "beerus: The account does not have enough money."},
		{[]string{"transfer", "--bank", "bank-a", "--from", a1, "--to", a1, "--amount", "5"}, "beerus: The from and to accounts are the same."},
		{[]string{"transfer", "--bank", "bank-a", "--from", a1, "--to", "9999999999", "--to-bank", "bank-b", "--amount", "5"}, "beerus: That is not a valid account at Bank B, so nothing was debited."},
		{[]string{"transfer", "--bank", "bank-a", "--from", a1, "--to", b1, "--to-bank", "bank-z", "--amount", "5"}, `beerus: Find bank "bank-z": not in the NIBSS directory.`},
		{[]string{"transfer", "--bank", "bank-a", "--from", a1}, "beerus: --from, --to and --amount are required; usage: beerus transfer --from NUBAN --to NUBAN --amount NAIRA [--from-bank ID] [--to-bank ID] [--narration TEXT]"},
		{[]string{"transfer", "--bogus"}, "beerus: flag provided but not defined: -bogus; usage: beerus transfer --from NUBAN --to NUBAN --amount NAIRA [--from-bank ID] [--to-bank ID] [--narration TEXT]"},
		{[]string{"customer", "create", "--bank", "bank-a", "--name", "X"}, "beerus: --name, --phone and --dob are required; usage: beerus customer create --name N --phone P --dob YYYY-MM-DD [--username U --password-stdin] | close <id>"},
		{[]string{"customer", "create", "--bank", "bank-a", "--name", "X", "--phone", "08031234567", "--dob", "soon"}, `beerus: Dob "soon" must be a date like 1990-04-21.`},
		{[]string{"customer", "close", adaID, "--bank", "bank-a"}, "beerus: Customer has open accounts: customer " + adaID + " still has 2 open accounts; close them first."},
		{[]string{"customer", "close", "cus_none", "--bank", "bank-a"}, "beerus: No such customer."},
		{[]string{"account", "open", "--bank", "bank-a", "--customer", adaID}, "beerus: --customer and --type are required; usage: beerus account open --customer ID [--type current|savings] | close <nuban>"},
		{[]string{"account", "close", a1, "--bank", "bank-a"}, "beerus: Account balance is not zero: account " + a1 + " holds ₦1,957.25; move it out by transfer or cash first."},
		{[]string{"account"}, "beerus: usage: beerus account open --customer ID [--type current|savings] | close <nuban>"},
		{[]string{"accounts", "--bank", "bank-a", "--limit", "0"}, "beerus: Limit must be a number from 1 to 1000."},
		{[]string{"accounts", "--bank", "bank-a", "--cursor", "junk"}, "beerus: That cursor is not valid. Use next_cursor from the last page."},
		{[]string{"statement", a1, "--bank", "bank-a", "--from", "tomorrow"}, `beerus: From "tomorrow" must be a date like 2026-03-01.`},
		{[]string{"fault", "timeout", "--bank", "bank-b", "--outcome", "maybe"}, `beerus: outcome "maybe" must be credited, failed or unknown`},
		{[]string{"fault", "down", "--bank", "bank-b", "--for", "soon"}, `beerus: for "soon" is not a duration above zero, such as 1h`},
		{[]string{"fault", "down", "--bank", "bank-z"}, "beerus: No such bank."},
		{[]string{"fault", "clear", "--bank", "bank-z"}, `beerus: No bank "bank-z".`},
		{[]string{"fault", "sideways"}, "beerus: usage: beerus fault down [--bank ID] [--for 1h] | timeout [--bank ID] --outcome credited|failed|unknown [--count N] | list [--bank ID] | clear [--bank ID]"},
		{[]string{"nip", "status", "000000000000"}, "beerus: No NIP session has that ID."},
		{[]string{"nip", "status"}, "beerus: usage: beerus nip status <session_id> | settle-now | name-enquiry <bank_code> <nuban> | participants | positions | settlement"},
		{[]string{"use"}, "beerus: usage: beerus use <bank>"},
		{[]string{"use", "bank-z"}, `beerus: bank "bank-z" is not in this setup; the banks are bank-a, bank-b`},
	}
	for _, tc := range cases {
		if got := c.fails(tc.args...); got != tc.want {
			t.Errorf("beerus %v:\n got %q\nwant %q", tc.args, got, tc.want)
		}
	}
}

type console struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	changed chan struct{}
	closed  bool
}

var ansi = regexp.MustCompile("\x1b\\[[0-9;?]*[a-zA-Z]|\x1b\\][^\a]*\a|\x1b[=>]|\r")

func startConsole(t *testing.T, c *cli, shell string, extra []string, args ...string) (*console, *os.File, *exec.Cmd) {
	t.Helper()
	cmd := exec.Command(c.bin, args...)
	cmd.Env = c.env(append([]string{"SHELL=" + shell}, extra...)...)
	tty, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 200})
	if err != nil {
		t.Fatalf("start beerus %v in a terminal: %v", args, err)
	}
	con := &console{changed: make(chan struct{}, 1)}
	go func() {
		chunk := make([]byte, 4096)
		for {
			n, err := tty.Read(chunk)
			con.mu.Lock()
			con.buf.Write(chunk[:n])
			con.closed = err != nil
			con.mu.Unlock()
			select {
			case con.changed <- struct{}{}:
			default:
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = tty.Close()
	})
	return con, tty, cmd
}

func (c *console) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return ansi.ReplaceAllString(c.buf.String(), "")
}

func (c *console) waitFor(t *testing.T, from int, pattern string) string {
	t.Helper()
	re := regexp.MustCompile(pattern)
	deadline := time.After(30 * time.Second)
	for {
		all := c.text()
		if from <= len(all) {
			if m := re.FindStringSubmatch(all[from:]); m != nil {
				return m[len(m)-1]
			}
		}
		select {
		case <-c.changed:
		case <-time.After(50 * time.Millisecond):
		case <-deadline:
			t.Fatalf("waited 30s for %q in the terminal output:\n%s", pattern, all)
		}
	}
}

func (c *console) send(t *testing.T, tty *os.File, s string) int {
	t.Helper()
	from := len(c.text())
	if _, err := tty.WriteString(s); err != nil {
		t.Fatalf("type %q: %v", s, err)
	}
	return from
}

func TestBankSession(t *testing.T) {
	s := newBankStack(t)
	c := newCLI(t, s)
	for _, name := range []string{"zsh", "bash"} {
		t.Run(name, func(t *testing.T) {
			shell, err := exec.LookPath(name)
			if err != nil {
				t.Skipf("%s is not installed", name)
			}
			home := t.TempDir()
			c.home = home
			userRC := map[string]string{".zshrc": "export USER_RC=zsh-rc\nPROMPT='user-prompt> '\n", ".zshenv": "export USER_ENV=zsh-env\n", ".bashrc": "export USER_RC=bash-rc\nPS1='user-prompt> '\n"}
			for file, body := range userRC {
				if err := os.WriteFile(filepath.Join(home, file), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			inputrc := filepath.Join(home, ".inputrc")
			if err := os.WriteFile(inputrc, []byte("set enable-bracketed-paste off\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			con, tty, cmd := startConsole(t, c, shell, []string{"INPUTRC=" + inputrc}, "use", "bank-a")
			con.waitFor(t, 0, `Using Bank A\. Business time: Thu 15 Jan 2026 08:00 WAT\. Type exit to leave\.`)
			con.waitFor(t, 0, `\(bank-a\) \$ `)

			from := con.send(t, tty, "echo file=$BEERUS_SESSION lvl=$SHLVL rc=$USER_RC\n")
			file := con.waitFor(t, from, `file=(\S+) lvl=\d+ rc=(?:zsh|bash)-rc`)
			info, err := os.Stat(file)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("session file %s: %v %v", file, info, err)
			}
			if raw, _ := os.ReadFile(file); string(raw) != "bank-a\n" {
				t.Errorf("session file holds %q", raw)
			}
			level := con.waitFor(t, from, `lvl=(\d+)`)

			from = con.send(t, tty, "beerus customers\n")
			con.waitFor(t, from, `No customers\.\s+Business time: Thu 15 Jan 2026 08:00 WAT`)
			from = con.send(t, tty, "beerus use bank-b\n")
			con.waitFor(t, from, `Switched to Bank B\. Business time: Thu 15 Jan 2026 08:00 WAT\. Type exit to leave\.`)
			con.waitFor(t, from, `\(bank-b\) \$ `)
			if raw, _ := os.ReadFile(file); string(raw) != "bank-b\n" {
				t.Errorf("session file after switching holds %q", raw)
			}
			from = con.send(t, tty, "echo again=$BEERUS_SESSION lvl=$SHLVL\n")
			con.waitFor(t, from, "again="+regexp.QuoteMeta(file)+` lvl=`+level+`\s`)
			from = con.send(t, tty, "beerus customers --bank bank-a --json\n")
			con.waitFor(t, from, `"data": \[`)

			from = con.send(t, tty, "sh -c 'echo running; exec sleep 30'\n")
			con.waitFor(t, from, `(?m)^running$`)
			from = con.send(t, tty, "\x03")
			con.waitFor(t, from, `\(bank-b\) \$ `)
			from = con.send(t, tty, "echo alive\n")
			con.waitFor(t, from, `alive[\s\S]*\(bank-b\) \$ `)

			from = con.send(t, tty, "\x03")
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("beerus use exited with %v", err)
				}
			case <-time.After(30 * time.Second):
				t.Fatalf("Ctrl-C at an empty prompt did not leave the session:\n%s", con.text()[from:])
			}
			if _, err := os.Stat(file); !os.IsNotExist(err) {
				t.Errorf("the session file %s was left behind: %v", file, err)
			}
			if _, err := os.Stat(filepath.Dir(file)); !os.IsNotExist(err) {
				t.Errorf("the session folder %s was left behind: %v", filepath.Dir(file), err)
			}
			for file, body := range userRC {
				if raw, err := os.ReadFile(filepath.Join(home, file)); err != nil || string(raw) != body {
					t.Errorf("%s was changed: %q %v", file, raw, err)
				}
			}
		})
	}
}
