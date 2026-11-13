package contracttest

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

const storySpec = `apiVersion: beerus.dev/v1
kind: Setup
metadata:
  name: story
spec:
  start: 2026-11-04T00:00:00+01:00
  nip:
    sessions: 1
    tsq_after: 2m
    tsq_attempts: 3
  banks:
    - id: bank-a
      name: Bank A
      type: commercial
    - id: bank-b
      name: Bank B
      type: digital
    - id: bank-c
      name: Bank C
      type: microfinance
  partners:
    - id: balance
      name: Balance
      legal_name: Evolution Labs
      purpose: to keep your books in sync with this account
      redirect_uris:
        - https://balance.localhost/bank_connections/callback
      webhook_url: %s
`

type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type session struct {
	t   *testing.T
	con *console
	tty *os.File
}

var sessionOutput = regexp.MustCompile(`(?ms)^@@begin\n(.*?)^@@rc=(\d+)\n`)

func (s *session) run(line string) (string, int) {
	s.t.Helper()
	from := s.con.send(s.t, s.tty, "echo @@begin; "+line+"; echo @@rc=$?\n")
	s.con.waitFor(s.t, from, `(?m)^@@rc=(\d+)\n`)
	m := sessionOutput.FindStringSubmatch(s.con.text()[from:])
	if m == nil {
		s.t.Fatalf("no output markers after %q in %q", line, s.con.text()[from:])
	}
	code, err := strconv.Atoi(m[2])
	if err != nil {
		s.t.Fatal(err)
	}
	return m[1], code
}

func (s *session) expect(line, pattern string) []string {
	s.t.Helper()
	out, code := s.run(line)
	m := regexp.MustCompile(`(?s)^` + pattern + `$`).FindStringSubmatch(out)
	if code != 0 || m == nil {
		s.t.Fatalf("%s\nexit %d, output %q\nwant exit 0 and a match for %q", line, code, out, pattern)
	}
	return m[1:]
}

func (s *session) refuse(line, want string) {
	s.t.Helper()
	if out, code := s.run(line); code != 1 || out != want+"\n" {
		s.t.Fatalf("%s\nexit %d, output %q\nwant exit 1 and %q", line, code, out, want)
	}
}

func quote(s string) string { return regexp.QuoteMeta(s) }

func startUp(t *testing.T, c *cli, spec string) (*lockedBuffer, *lockedBuffer) {
	t.Helper()
	args, name := []string{"up", "-f", spec}, "story"
	if spec == "" {
		args, name = []string{"up"}, "beerus"
		c.ok("init")
	} else {
		c.ok("init", "-f", spec)
	}
	cmd := exec.Command(c.bin, args...)
	cmd.Env = c.env()
	out, errOut := &lockedBuffer{}, &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = out, errOut
	if err := cmd.Start(); err != nil {
		t.Fatalf("start beerus up: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGINT)
		select {
		case <-exited:
		case <-time.After(30 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
		if got, errOut, code := c.run("reset", name); code != 0 || !strings.HasPrefix(got, "Dropped databases:\n") {
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
	return out, errOut
}

func dropBank(t *testing.T, dir, id string) {
	t.Helper()
	path := filepath.Join(dir, "setup.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?m)^    - id: ` + quote(id) + `\n(?:      .*\n)+`)
	if !block.Match(raw) {
		t.Fatalf("%s has no bank %s:\n%s", path, id, raw)
	}
	if err := os.WriteFile(path, block.ReplaceAll(raw, nil), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestStory(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a full stack; run without -short")
	}
	if u := os.Getenv("BEERUS_TEST_DATABASE_URL"); u != "" {
		t.Setenv(config.DatabaseURLEnv, u)
	}
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	sink := newSink()
	defer sink.srv.Close()
	root := t.TempDir()
	dir := filepath.Join(root, "beerus")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ports := stackPorts(t, 3)
	local := fmt.Sprintf("database_url: postgres:///postgres\nconnect:\n  host: connect.localhost\n  partner_port: %d\n  widget_port: %d\n  control_port: %d\n  health_port: %d\n  sync_interval: 1m\n  refresh_min_interval: 1m\n", ports[0], ports[1], ports[2], ports[3])
	if err := os.WriteFile(filepath.Join(dir, "local.yaml"), []byte(local), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := filepath.Join(root, "story.yaml")
	if err := os.WriteFile(spec, []byte(fmt.Sprintf(storySpec, sink.srv.URL+"/webhooks")), 0o600); err != nil {
		t.Fatal(err)
	}
	c := newCLIAt(t, dir)
	const (
		at       = `Business time: [A-Z][a-z]{2} \d{1,2} [A-Z][a-z]{2} \d{4} \d\d:\d\d WAT\n`
		hm       = `[A-Z][a-z]{2} \d{1,2} [A-Z][a-z]{2} \d{4} \d\d:\d\d`
		settleAt = `adhoc:\S+`
	)

	out, errOut := startUp(t, c, spec)
	if !regexp.MustCompile(`^Beerus is running\. [A-Z][a-z]{2} \d{1,2} [A-Z][a-z]{2} \d{4} \d\d:\d\d WAT\.\n\n[\s\S]*\nPress Ctrl\+C to stop\.\n$`).MatchString(out.String()) || strings.Count(out.String(), "Partner API") != 1 || strings.Contains(errOut.String(), "level=ERROR") {
		t.Fatalf("up printed %q and logged %q", out.String(), errOut.String())
	}
	if got := c.ok("diff"); got != "No changes.\n" {
		t.Errorf("diff after up: %q", got)
	}
	if got := c.ok("get", "banks"); !regexp.MustCompile(`^ID +NAME +PHASE +READY +GEN +MESSAGE\nbank-a +Bank A +active +yes +1 *\nbank-b +Bank B +active +yes +1 *\nbank-c +Bank C +active +yes +1 *\n$`).MatchString(got) {
		t.Errorf("get banks after up: %q", got)
	}
	if got := c.fails("clock"); got != "beerus: unknown command \"clock\". Run beerus help." {
		t.Errorf("clock: %q", got)
	}

	t.Setenv("BEERUS_CONTRACT_DIR", dir)
	s := startStack(t)
	bankB := *s.cfg.Bank("bank-b")
	bankC := *s.cfg.Bank("bank-c")

	home := t.TempDir()
	inputrc := filepath.Join(home, ".inputrc")
	if err := os.WriteFile(inputrc, []byte("set enable-bracketed-paste off\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte("PS1='user-prompt> '\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.home = home
	con, tty, cmd := startConsole(t, c, shell, []string{"INPUTRC=" + inputrc}, "use", "bank-a")
	con.waitFor(t, 0, `Using Bank A\. Business time: [A-Z][a-z]{2} \d{1,2} [A-Z][a-z]{2} \d{4} \d\d:\d\d WAT\. Type exit to leave\.`)
	con.waitFor(t, 0, `\(bank-a\) \$ `)
	sh := &session{t: t, con: con, tty: tty}
	sh.refuse("beerus customers --bank bank-z", "beerus: No such bank.")

	ada := sh.expect(`beerus customer create --name "Adaeze Okafor" --phone 08031234567 --dob 1988-03-14`, `Created customer (cus_\w+), Adaeze Okafor, at bank-a\. BVN (\d+)\.\n`+at)
	adaA, bvn := ada[0], ada[1]
	if got := sh.expect("beerus customers", `ID +NAME +PHONE +BVN +STATUS\n`+quote(adaA)+` +Adaeze Okafor +2348031234567 +`+bvn+` +active\n1 customer on this page\.\n`+at); got == nil {
		t.Fatal("no customers")
	}
	nubA := sh.expect("beerus account open --customer "+adaA+" --type current", `Opened current account (\d{10}) for Adaeze Okafor \(`+adaA+`\) at bank-a\. Balance ₦0\.00\.\n`+at)[0]
	sh.expect("beerus deposit "+nubA+" --amount 500000", `Deposited ₦500,000\.00 into account `+nubA+` at bank-a\. Balance ₦500,000\.00\.\n`+at)

	sh.expect("beerus use bank-b", `Switched to Bank B\. Business time: [A-Z][a-z]{2} \d{1,2} [A-Z][a-z]{2} \d{4} \d\d:\d\d WAT\. Type exit to leave\.\n`)
	con.waitFor(t, 0, `\(bank-b\) \$ `)
	adaB := sh.expect("echo beerus-test-adaeze | beerus customer create --name \"Adaeze Okafor\" --phone 08031234567 --dob 1988-03-14 --username adaeze --password-stdin", `Created customer (cus_\w+), Adaeze Okafor, at bank-b\. BVN `+bvn+`\.\nAdaeze Okafor can sign in to online banking as adaeze\.\n`+at)[0]
	chidi := sh.expect(`beerus customer create --name "Chidi Okafor" --phone 08034567890 --dob 1995-05-30`, `Created customer (cus_\w+), Chidi Okafor, at bank-b\. BVN (\d+)\.\n`+at)[0]
	nubAdaB := sh.expect("beerus account open --customer "+adaB+" --type savings", `Opened savings account (\d{10}) for Adaeze Okafor \(`+adaB+`\) at bank-b\. Balance ₦0\.00\.\n`+at)[0]
	nubChidi := sh.expect("beerus account open --customer "+chidi+" --type current", `Opened current account (\d{10}) for Chidi Okafor \(`+chidi+`\) at bank-b\. Balance ₦0\.00\.\n`+at)[0]

	sh.expect("beerus use bank-c", `Switched to Bank C\. Business time: [A-Z][a-z]{2} \d{1,2} [A-Z][a-z]{2} \d{4} \d\d:\d\d WAT\. Type exit to leave\.\n`)
	tunde := sh.expect(`beerus customer create --name "Tunde Motors" --phone 08032345678 --dob 1979-07-02`, `Created customer (cus_\w+), Tunde Motors, at bank-c\. BVN (\d+)\.\n`+at)[0]
	nubTunde := sh.expect("beerus account open --customer "+tunde+" --type current", `Opened current account (\d{10}) for Tunde Motors \(`+tunde+`\) at bank-c\. Balance ₦0\.00\.\n`+at)[0]
	sh.expect("beerus deposit "+nubTunde+" --amount 3000", `Deposited ₦3,000\.00 into account `+nubTunde+` at bank-c\. Balance ₦3,000\.00\.\n`+at)
	sh.expect("beerus use bank-a", `Switched to Bank A\. Business time: [A-Z][a-z]{2} \d{1,2} [A-Z][a-z]{2} \d{4} \d\d:\d\d WAT\. Type exit to leave\.\n`)

	sh.refuse("beerus transfer --from "+nubA+" --to 9999999999 --to-bank bank-b --amount 5", "beerus: That is not a valid account at Bank B, so nothing was debited.")
	first := sh.expect("beerus transfer --from "+nubA+" --to "+nubChidi+" --to-bank bank-b --amount 5000 --narration rent", `Sending ₦5,000\.00 to Chidi Okafor at Bank B\.\nSession (\d+): success at `+hm+` WAT\nTotal debit ₦5,010\.75: fee ₦10\.00, VAT ₦0\.75 and stamp duty ₦0\.00\. Balance ₦494,989\.25\.\n`)[0]

	sh.expect("beerus fault timeout --bank bank-b --outcome credited", `Fault flt_\w+ on bank-b: the next transfer times out \(outcome credited\)\.\n`+at)
	if got := sh.expect("beerus fault list", `ID +BANK +WHAT\nflt_\w+ +bank-b +the next transfer times out \(outcome credited\)\n`+at); got == nil {
		t.Fatal("no fault listed")
	}
	pending := sh.expect("beerus transfer --from "+nubA+" --to "+nubAdaB+" --to-bank bank-b --amount 1000", `Sending ₦1,000\.00 to Adaeze Okafor at Bank B\.\nSession (\d+): pending\. Beerus will check with NIBSS at `+hm+` WAT\. Run beerus nip status (\d+)\.\nTotal debit ₦1,010\.75: fee ₦10\.00, VAT ₦0\.75 and stamp duty ₦0\.00\. Balance ₦493,978\.50\.\n`)
	if pending[0] != pending[1] {
		t.Fatalf("the pending transfer names two sessions: %v", pending)
	}
	sh.expect("beerus fault list", `No faults\.\n`+at)
	sh.expect("beerus nip settlement", `\{\n  "problems": \[\],\n  "sessions": 0\n\}\n`)
	sh.expect("beerus nip status "+pending[0], `Session `+pending[0]+`: was pending; now success at `+hm+` WAT\nAmount ₦1,000\.00 from `+nubA+` \(bank code 000001\) to `+nubAdaB+` \(bank code 000002\), started at `+hm+` WAT\.\n`)
	sh.expect("beerus nip settle-now", `Settled 1 session at `+hm+` WAT\.\n`)
	sh.expect("beerus nip settlement", `\{\n  "problems": \[\],\n  "sessions": 1\n\}\n`)
	sh.expect("beerus nip status "+first, `Session `+first+`: success at `+hm+` WAT\nAmount ₦5,000\.00 from `+nubA+` \(bank code 000001\) to `+nubChidi+` \(bank code 000002\), started at `+hm+` WAT\.\nSettled in settlement session `+settleAt+`\.\n`)
	sh.expect("beerus nip settle-now", `No sessions to settle at `+hm+` WAT\.\n`)

	sh.expect("beerus transfer --from "+nubA+" --to "+nubTunde+" --to-bank bank-c --amount 4000", `Sending ₦4,000\.00 to Tunde Motors at Bank C\.\nSession \d+: success at `+hm+` WAT\nTotal debit ₦4,010\.75: fee ₦10\.00, VAT ₦0\.75 and stamp duty ₦0\.00\. Balance ₦489,967\.75\.\n`)
	dropBank(t, dir, "bank-c")
	out1, err1, code1 := c.run("apply", "--prune", "--money", "transfer", "--to", "bank-a", "--settle-now", "--wait", "60s")
	wantPlanC := "Setup story\n\n  - bank bank-c      remove   DESTRUCTIVE\n        stop routing; settle 1 open transfer (net ₦4,000.00);\n        transfer ₦7,000.00 for 1 customer to Bank A (on_remove); settle the moves;\n        drop 1 database. Its ID and codes stay reserved.\n\nPlan: 1 to remove.\n"
	if code1 != 0 || !regexp.MustCompile(`^`+quote(wantPlanC)+`Money of Bank C on `+hm+` WAT:\n  Tunde Motors +\d{10} +₦7,000\.00 +transferred to Bank A account \d{10}\n$`).MatchString(out1) || err1 != "" {
		t.Fatalf("removing bank-c with --settle-now: exit %d, output %q, error %q", code1, out1, err1)
	}
	if got := c.ok("get", "banks"); !regexp.MustCompile(`^ID +NAME +PHASE +READY +GEN +MESSAGE\nbank-a +Bank A +active +yes +1 *\nbank-b +Bank B +active +yes +1 *\n$`).MatchString(got) {
		t.Errorf("get banks after removing bank-c: %q", got)
	}
	sh.expect("beerus nip settlement", `\{\n  "problems": \[\],\n  "sessions": 3\n\}\n`)
	sh.expect("beerus accounts", `NUBAN +OWNER +CUSTOMER +TYPE +STATUS +BALANCE\n`+nubA+` +Adaeze Okafor +`+adaA+` +current +active +₦489,967\.75\n(\d{10}) +Tunde Motors +`+`cus_\w+`+` +current +active +₦7,000\.00\n2 accounts on this page\.\n`+at)
	s.people["adaeze@bank-b"] = persona{ID: "adaeze", Bank: "bank-b", Name: "Adaeze Okafor", Username: "adaeze", Password: "beerus-test-adaeze", Customer: adaB, BVN: bvn, Accounts: []string{nubAdaB}}
	conn := s.connect("adaeze", "bank-b", s.today().AddDays(-3))
	acct := fmt.Sprint(conn.Accounts[0]["id"])

	sh.expect("beerus transfer --from "+nubA+" --to "+nubChidi+" --to-bank bank-b --amount 2000", `Sending ₦2,000\.00 to Chidi Okafor at Bank B\.\nSession \d+: success at `+hm+` WAT\nTotal debit ₦2,010\.75: fee ₦10\.00, VAT ₦0\.75 and stamp duty ₦0\.00\. Balance ₦487,957\.00\.\n`)
	sh.expect("beerus fault timeout --bank bank-b --outcome credited", `Fault flt_\w+ on bank-b: the next transfer times out \(outcome credited\)\.\n`+at)
	late := sh.expect("beerus transfer --from "+nubA+" --to "+nubAdaB+" --to-bank bank-b --amount 300", `Sending ₦300\.00 to Adaeze Okafor at Bank B\.\nSession (\d+): pending\. Beerus will check with NIBSS at `+hm+` WAT\. Run beerus nip status \d+\.\nTotal debit ₦310\.75: fee ₦10\.00, VAT ₦0\.75 and stamp duty ₦0\.00\. Balance ₦487,646\.25\.\n`)[0]

	dropBank(t, dir, "bank-b")
	if got := c.fails("apply"); got != "beerus: The plan removes bank-b. Nothing was changed. Run again with --prune to remove it." {
		t.Errorf("removal without --prune: %q", got)
	}
	settlePending := "SettlementPending: Waiting for the 00:00 NIP session to settle 2 transfers (net ₦2,300.00). Wait for it, or run beerus nip settle-now."
	out2, err2, code2 := c.run("apply", "--prune", "--wait", "1s")
	wantPlanB := "Setup story\n\n  - bank bank-b      remove   DESTRUCTIVE\n        stop routing; revoke 1 connection; settle 2 open transfers (net ₦2,300.00);\n        ₦8,300.00 for 2 customers needs a money decision: add on_remove or run with --money;\n        drop 1 database. Its ID and codes stay reserved.\n\nPlan: 1 to remove.\n"
	if code2 != 3 || out2 != wantPlanB || !regexp.MustCompile(`^(bank bank-b: `+quote(settlePending)+`\n)?beerus: bank bank-b: `+quote(settlePending)+`$`).MatchString(err2) {
		t.Fatalf("removing bank-b with --prune --wait 1s: exit %d, output %q, error %q", code2, out2, err2)
	}

	unavailable := `\{\n  "account_number": "` + nubChidi + `",\n  "bank_code": "000002",\n  "bank_name": "Bank B",\n  "message": "Bank B is not available right now\.",\n  "response_code": "91"\n\}\n`
	sh.expect("beerus nip name-enquiry 000002 "+nubChidi, unavailable)
	sh.refuse("beerus transfer --from "+nubA+" --to "+nubChidi+" --to-bank bank-b --amount 10", "beerus: Bank B is not available right now, so nothing was debited.")
	revoked := func() []map[string]any {
		return s.eventsOf("connection.status_changed", func(d map[string]any) bool {
			return d["connection_id"] == conn.ConnectionID && d["status"] == "revoked"
		})
	}
	waitFor(t, "one connection.revoked webhook", func() bool { return len(revoked()) == 1 })
	s.must(s.call("GET", "/v1/accounts/"+acct+"/balance", nil), 409)
	if got := c.ok("get", "banks"); !regexp.MustCompile(`bank-b +Bank B +terminating +no +2 +bank bank-b: ` + quote(settlePending)).MatchString(got) {
		t.Errorf("get banks while terminating: %q", got)
	}
	readd := filepath.Join(root, "readd.yaml")
	if err := os.WriteFile(readd, []byte(strings.Replace(strings.Replace(fmt.Sprintf(storySpec, sink.srv.URL+"/webhooks"), "    - id: bank-c\n      name: Bank C\n      type: microfinance\n", "", 1), "", "", 0)), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := c.fails("apply", "-f", readd); !regexp.MustCompile(`^beerus: ` + quote(readd) + `:\d+:\d+: banks\[bank-b\] is being removed, and its id is never reused\. Choose another id\.$`).MatchString(got) {
		t.Errorf("re-adding bank-b while it is terminating: %q", got)
	}

	sh.expect("beerus nip status "+late, `Session `+late+`: was pending; now success at `+hm+` WAT\nAmount ₦300\.00 from `+nubA+` \(bank code 000001\) to `+nubAdaB+` \(bank code 000002\), started at `+hm+` WAT\.\n`)
	sh.expect("beerus nip settle-now", `Settled 1 session at `+hm+` WAT\.\n`)
	moneyDecision := `bank bank-b: MoneyDecisionNeeded: Bank B holds ₦8,300.00 for 2 customers. Choose --money transfer --to <bank>, --money payout or --money leave. Nothing moves until you do.`
	waitFor(t, "the money decision to be asked", func() bool { return strings.Contains(c.ok("get", "banks"), moneyDecision) })
	if out, errText, code := c.run("apply", "--prune", "--money", "transfer", "--to", "bank-a", "--wait", "0"); code != 0 || errText != "" || out != "Setup story\n\n  ~ bank bank-b      update   on_remove.money +transfer; on_remove.to +bank-a\n\nPlan: 1 to update.\n" {
		t.Fatalf("money decision: exit %d, output %q, error %q", code, out, errText)
	}
	waitFor(t, "the moves to wait for settlement", func() bool {
		return strings.Contains(c.ok("get", "banks"), "SettlementPending: Waiting for the 00:00 NIP session to settle 2 transfers (net ₦8,300.00).")
	})
	sh.expect("beerus nip settle-now", `Settled 1 session at `+hm+` WAT\.\n`)
	waitFor(t, "bank-b to be removed", func() bool { return !strings.Contains(c.ok("get", "banks"), "bank-b") })
	if got := c.ok("get", "banks"); !regexp.MustCompile(`^ID +NAME +PHASE +READY +GEN +MESSAGE\nbank-a +Bank A +active +yes +1 *\n$`).MatchString(got) {
		t.Errorf("get banks at the end: %q", got)
	}

	sh.expect("beerus accounts", `NUBAN +OWNER +CUSTOMER +TYPE +STATUS +BALANCE\n`+nubA+` +Adaeze Okafor +`+adaA+` +current +active +₦487,646\.25\n(\d{10}) +Tunde Motors +`+`cus_\w+`+` +current +active +₦7,000\.00\n(\d{10}) +Adaeze Okafor +`+adaA+` +savings +active +₦1,300\.00\n(\d{10}) +Chidi Okafor +cus_\w+ +current +active +₦7,000\.00\n4 accounts on this page\.\n`+at)
	if out, code := sh.run("beerus statement " + nubA); code != 0 || !strings.Contains(out, "NIP TRF TO Chidi Okafor/Bank B/") {
		t.Errorf("statement after the removal: exit %d, %q", code, out)
	}
	sh.expect("beerus nip name-enquiry 000002 "+nubChidi, unavailable)
	sh.expect("beerus nip settlement", `\{\n  "problems": \[\],\n  "sessions": 5\n\}\n`)
	if len(revoked()) != 1 {
		t.Errorf("revoked webhooks after the removal: %v", revoked())
	}
	for _, b := range []config.Bank{bankB, bankC} {
		names, err := store.List(context.Background(), s.cfg.DatabaseURL, b.Database)
		if err != nil || len(names) != 0 {
			t.Errorf("databases of %s after its removal: %v %v", b.ID, names, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "banks", b.ID)); !os.IsNotExist(err) {
			t.Errorf("banks/%s after its removal: %v", b.ID, err)
		}
		for _, port := range []int{b.Port, b.APIPort} {
			l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
			if err != nil {
				t.Errorf("port %d of %s is still taken: %v", port, b.ID, err)
				continue
			}
			l.Close()
		}
	}
	if err := os.WriteFile(readd, []byte(fmt.Sprintf(storySpec, sink.srv.URL+"/webhooks")), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := c.fails("apply", "-f", readd); !strings.Contains(got, "banks[bank-b].id bank-b belonged to a removed bank. Choose another id.") {
		t.Errorf("re-adding bank-b after its removal: %q", got)
	}
	if got := c.ok("doctor"); !strings.HasSuffix(got, "Nothing is left behind.\n") {
		t.Errorf("doctor: %q", got)
	}
	con.send(t, tty, "exit\n")
	left := make(chan error, 1)
	go func() { left <- cmd.Wait() }()
	select {
	case err := <-left:
		if err != nil {
			t.Errorf("beerus use exited with %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Error("exit did not leave the session")
	}
}
