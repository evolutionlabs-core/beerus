package contracttest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/install"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

const (
	probeTimeout = 5 * time.Second
	stoppedBank  = "beerus: Bank A is not running or cannot be reached. Run beerus get banks to see its state, then try again."
	noBanks      = "beerus: there are no banks yet. Add one first with beerus bank create --name NAME"
)

var participantProcess = regexp.MustCompile(`^\s*(\d+) .* run --participant (\S+) `)

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

func blankDir(t *testing.T) string {
	t.Helper()
	if u := os.Getenv("BEERUS_TEST_DATABASE_URL"); u != "" {
		t.Setenv(config.DatabaseURLEnv, u)
	}
	dir := filepath.Join(t.TempDir(), "beerus")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := stackPorts(t, 1)
	local := fmt.Sprintf("database_url: postgres:///postgres\nconnect:\n  host: connect.localhost\n  partner_port: %d\n  widget_port: %d\n  control_port: %d\n  health_port: %d\n  nibss_port: %d\n  cbn_port: %d\n  connect_control_port: %d\n  sync_interval: 1m\n  refresh_min_interval: 1m\n", p[0], p[1], p[2], p[3], p[4], p[5], p[7])
	if err := os.WriteFile(filepath.Join(dir, "local.yaml"), []byte(local), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func startUp(t *testing.T, c *cli) (*lockedBuffer, *lockedBuffer, *exec.Cmd, chan error) {
	t.Helper()
	c.ok("init")
	cmd := exec.Command(c.bin, "up")
	cmd.Env = c.env()
	out, errOut := &lockedBuffer{}, &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = out, errOut
	if err := cmd.Start(); err != nil {
		t.Fatalf("start beerus up: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("up stderr:\n%s", errOut.String())
		}
		_ = cmd.Process.Signal(syscall.SIGINT)
		select {
		case <-exited:
		case <-time.After(60 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
		if _, errText, code := c.run("reset", "beerus"); code != 0 {
			t.Errorf("reset: exit %d, error %q", code, errText)
		}
	})
	waitFor(t, waitLimit, "beerus up to print that it is running", func() bool {
		select {
		case err := <-exited:
			t.Fatalf("beerus up exited: %v\n%s\n%s", err, out.String(), errOut.String())
		default:
		}
		return strings.HasSuffix(out.String(), "\nPress Ctrl+C to stop.\n")
	})
	return out, errOut, cmd, exited
}

type bankProbe struct {
	t            *testing.T
	site, api    string
	nuban        string
	token        string
	nibss, plain *http.Client
	agg          *http.Client
}

func (p *bankProbe) get(client *http.Client, url string, header http.Header) int {
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}

func (p *bankProbe) phase() string {
	req, _ := http.NewRequest(http.MethodGet, p.site+"/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+p.token)
	resp, err := p.plain.Do(req)
	if err != nil {
		return err.Error()
	}
	defer resp.Body.Close()
	var st struct{ Phase string }
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &st) != nil {
		return fmt.Sprintf("status %d", resp.StatusCode)
	}
	return st.Phase
}

func (p *bankProbe) want(what string, ui, nip, ob int, phase string) {
	p.t.Helper()
	waitFor(p.t, waitLimit, what, func() bool {
		return p.get(p.plain, p.site+"/login", nil) == ui && p.get(p.nibss, p.api+"/v1/nip/account-names/"+p.nuban, nil) == nip && p.get(p.agg, p.api+"/", nil) == ob && p.phase() == phase
	})
}

func controlCall(t *testing.T, cfg *config.Config, method, path string, body any) (int, map[string]any) {
	t.Helper()
	token, err := config.ReadSecret(cfg.Path("control-token"))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(t.Context(), method, cfg.ControlURL()+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", web.NewID("key"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, out
}

func createServing(t *testing.T, cfg *config.Config, name, id string) {
	t.Helper()
	if code, out := controlCall(t, cfg, http.MethodPost, "/banks", map[string]string{"name": name}); code >= http.StatusMultipleChoices {
		t.Fatalf("create %s: %d %v", name, code, out)
	}
	defer func() {
		if t.Failed() {
			_, out := controlCall(t, cfg, http.MethodGet, "/banks", nil)
			t.Logf("banks: %v", out)
		}
	}()
	waitFor(t, waitLimit, id+" to turn active", func() bool {
		_, out := controlCall(t, cfg, http.MethodGet, "/banks", nil)
		rows, _ := out["data"].([]any)
		for _, r := range rows {
			if row, _ := r.(map[string]any); row["id"] == id {
				return row["phase"] == config.PhaseActive
			}
		}
		return false
	})
	body := map[string]string{"name": "Ada Obi", "phone": "08030000000", "dob": "1990-04-12"}
	if code, out := controlCall(t, cfg, http.MethodPost, "/banks/"+id+"/customers", body); code != http.StatusCreated {
		t.Fatalf("customer at %s the moment it turned active: %d %v", id, code, out)
	}
}

func TestProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a full stack; run without -short")
	}
	if os.Getenv("BEERUS_CONTRACT_DIR") != "" {
		t.Skip("needs an in-process stack")
	}
	dir := blankDir(t)
	c := newCLIAt(t, dir)
	out, errOut, up, exited := startUp(t, c)
	if text := out.String(); strings.Contains(text, "online banking") || !strings.Contains(text, "\nNo banks yet. Add the first one with beerus bank create --name NAME.\n") || strings.Contains(errOut.String(), "level=ERROR") {
		t.Fatalf("up printed %q and logged %q", text, errOut.String())
	}
	cfg, err := setup.LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.fails("use", "bank-a"); got != noBanks {
		t.Errorf("use before any bank: %q", got)
	}
	if err := install.CheckPorts(cfg); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("a second start while running: %v", err)
	}

	c.ok("bank", "create", "--name", "Bank A")
	customer, _ := c.capture(`Created customer (cus_\w+), Ada Obi, at bank-a`, "customer", "create", "--bank", "bank-a", "--name", "Ada Obi", "--phone", "08030000000", "--dob", "1990-04-12")
	account, _ := c.capture(`Opened savings account (\d{10}) for Ada Obi`, "account", "open", "--bank", "bank-a", "--customer", customer, "--type", "savings")
	c.ok("deposit", account, "--bank", "bank-a", "--amount", "5,000.50")
	token, err := config.ReadSecret(filepath.Join(dir, "control-token"))
	if err != nil {
		t.Fatal(err)
	}
	row := c.printedJSON("bank", "list")["data"].([]any)[0].(map[string]any)
	p := &bankProbe{t: t, site: fmt.Sprintf("http://127.0.0.1:%d", int(row["port"].(float64))), api: fmt.Sprintf("http://127.0.0.1:%d", int(row["api_port"].(float64))), nuban: account, token: token, nibss: callerClient(t, config.NIBSSID), agg: callerClient(t, config.ConnectID), plain: &http.Client{Timeout: probeTimeout}}
	p.want("the bank to open once it has its phase", 200, 200, 404, "active")
	c.ok("bank", "suspend", "bank-a")
	p.want("a suspended bank to refuse the site, NIP and open banking", 503, 503, 503, "suspended")
	c.ok("bank", "resume", "bank-a")
	c.ok("fault", "down", "--bank", "bank-a")
	p.want("a down bank to serve the site but refuse NIP and open banking", 200, 503, 503, "active")
	c.ok("fault", "clear")
	p.want("a bank without faults to answer again", 200, 200, 404, "active")

	accounts, statement := c.ok("accounts", "--bank", "bank-a"), c.ok("statement", account, "--bank", "bank-a")
	old := processesIn(t, dir)["bank-a"]
	if err := syscall.Kill(old, syscall.SIGKILL); err != nil {
		t.Fatalf("kill bank-a: %v", err)
	}
	waitFor(t, waitLimit, "bank-a to come back with the same books", func() bool {
		pid := processesIn(t, dir)["bank-a"]
		a, _, aCode := c.run("accounts", "--bank", "bank-a")
		s, _, sCode := c.run("statement", account, "--bank", "bank-a")
		return pid != 0 && pid != old && aCode == 0 && sCode == 0 && a == accounts && s == statement
	})
	bankPID, oldCore := processesIn(t, dir)["bank-a"], processesIn(t, dir)["core"]
	if err := syscall.Kill(oldCore, syscall.SIGKILL); err != nil {
		t.Fatalf("kill core: %v", err)
	}
	waitFor(t, waitLimit, "core to come back and a deposit to work", func() bool {
		got, _, code := c.run("deposit", account, "--bank", "bank-a", "--amount", "1")
		return processesIn(t, dir)["core"] != oldCore && code == 0 && strings.Contains(got, "Balance ₦5,001.50.")
	})
	if got := processesIn(t, dir)["bank-a"]; got != bankPID {
		t.Errorf("bank-a restarted when core was killed: pid %d, then %d", bankPID, got)
	}

	pattern := "--participant bank-a --dir " + dir
	if out, err := exec.Command("pkill", "-STOP", "-f", "--", pattern).CombinedOutput(); err != nil {
		t.Fatalf("freeze bank-a: %v %s", err, out)
	}
	thaw := func() {
		if out, err := exec.Command("pkill", "-CONT", "-f", "--", pattern).CombinedOutput(); err != nil {
			t.Errorf("thaw bank-a: %v %s", err, out)
		}
	}
	var thawed sync.Once
	t.Cleanup(func() { thawed.Do(thaw) })
	if got := c.fails("accounts", "--bank", "bank-a"); got != stoppedBank {
		t.Errorf("a call while the bank is stopped: %q", got)
	}
	createServing(t, cfg, "Bank B", "bank-b")
	thawed.Do(thaw)
	waitFor(t, waitLimit, "the thawed bank to answer", func() bool {
		got, _, code := c.run("accounts", "--bank", "bank-a")
		return code == 0 && strings.Contains(got, "₦5,001.50")
	})

	if got := c.fails("bank", "purge", "bank-b"); !strings.Contains(got, "--yes") {
		t.Errorf("purge without --yes outside a terminal: %q", got)
	}
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	con, tty, cmd := startConsole(t, c, shell, nil, "bank", "purge", "bank-b", "--money", "leave")
	con.waitFor(t, 0, `Type bank-b to confirm: `)
	con.send(t, tty, "bank-b\n")
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("purge at the prompt: %v\n%s", err, con.text())
		}
	case <-time.After(60 * time.Second):
		t.Fatalf("purge at the prompt did not finish:\n%s", con.text())
	}
	if got := c.ok("get", "banks"); strings.Contains(got, "bank-b") {
		t.Errorf("get banks after the purge: %q", got)
	}

	if err := up.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("interrupt beerus up: %v", err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Errorf("beerus up after Ctrl+C: %v\n%s", err, errOut.String())
		}
	case <-time.After(60 * time.Second):
		t.Fatalf("beerus up did not stop\n%s", errOut.String())
	}
	if left := processesIn(t, dir); len(left) != 0 {
		t.Errorf("participants left after up exited: %v", left)
	}
}
