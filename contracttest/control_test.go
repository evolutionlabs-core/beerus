package contracttest

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
)

const controlProbeTimeout = 5 * time.Second

type bankProbe struct {
	t      *testing.T
	site   string
	api    string
	nuban  string
	token  string
	nibss  *http.Client
	agg    *http.Client
	plain  *http.Client
	host   string
	status string
}

func mutualClient(t *testing.T, dir, certDir, name string) *http.Client {
	t.Helper()
	pair, err := tls.LoadX509KeyPair(filepath.Join(dir, certDir, name+".crt"), filepath.Join(dir, certDir, name+".key"))
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.ReadFile(filepath.Join(dir, "ca", "root.crt"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(root)
	tr := &http.Transport{TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{pair}, RootCAs: pool, ServerName: "bank-a.localhost", MinVersion: tls.VersionTLS12}, IdleConnTimeout: controlProbeTimeout, DisableKeepAlives: true}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: controlProbeTimeout}
}

func (p *bankProbe) get(client *http.Client, url string, header http.Header) (int, string) {
	p.t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		p.t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err.Error()
	}
	return resp.StatusCode, strings.TrimSpace(string(raw))
}

func (p *bankProbe) ui() int {
	code, _ := p.get(p.plain, p.site+"/login", nil)
	return code
}

func (p *bankProbe) nip() int {
	code, _ := p.get(p.nibss, p.api+"/v1/nip/account-names/"+p.nuban, nil)
	return code
}

func (p *bankProbe) openBanking() int {
	code, _ := p.get(p.agg, p.api+"/", nil)
	return code
}

func (p *bankProbe) phase() string {
	code, body := p.get(p.plain, p.site+"/v1/status", http.Header{"Authorization": {"Bearer " + p.token}})
	if code != http.StatusOK {
		return fmt.Sprintf("status %d", code)
	}
	var st struct{ Phase string }
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		return err.Error()
	}
	return st.Phase
}

func (p *bankProbe) want(what string, ui, nip, ob int, phase string) {
	p.t.Helper()
	waitFor(p.t, what, func() bool {
		return p.ui() == ui && p.nip() == nip && p.openBanking() == ob && p.phase() == phase
	})
}

func TestBankControlOnARealUp(t *testing.T) {
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
	t.Cleanup(func() {
		_ = up.Process.Signal(syscall.SIGINT)
		select {
		case <-exited:
		case <-time.After(60 * time.Second):
			_ = up.Process.Kill()
			<-exited
		}
		c.run("reset", "beerus")
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
	token, err := config.ReadSecret(filepath.Join(dir, "control-token"))
	if err != nil {
		t.Fatal(err)
	}
	banks := c.printedJSON("bank", "list")["data"].([]any)
	bank := banks[0].(map[string]any)
	p := &bankProbe{
		t:     t,
		site:  fmt.Sprintf("http://127.0.0.1:%d", int(bank["port"].(float64))),
		api:   fmt.Sprintf("https://127.0.0.1:%d", int(bank["api_port"].(float64))),
		nuban: account,
		token: token,
		nibss: mutualClient(t, dir, "nibss", "client"),
		agg:   mutualClient(t, dir, "connect", "client"),
		plain: &http.Client{Timeout: controlProbeTimeout},
	}
	p.want("the bank to open once it has its phase", 200, 200, 404, "active")

	c.ok("bank", "suspend", "bank-a")
	p.want("a suspended bank to refuse the site, NIP and open banking", 503, 503, 503, "suspended")
	c.ok("bank", "resume", "bank-a")
	p.want("a resumed bank to open again", 200, 200, 404, "active")

	c.ok("fault", "down", "--bank", "bank-a")
	waitFor(t, "a down bank to refuse NIP calls", func() bool { return p.nip() == 503 && p.openBanking() == 503 })
	if got := p.ui(); got != 200 {
		t.Fatalf("the site of a down bank answered %d, want 200", got)
	}
	c.ok("fault", "clear")
	p.want("a bank without faults to answer NIP calls again", 200, 200, 404, "active")

	c.ok("fault", "down", "--bank", "bank-a")
	c.ok("bank", "suspend", "bank-a")
	p.want("a suspended and down bank to refuse everything", 503, 503, 503, "suspended")
	old := processesIn(t, dir)["bank-a"]
	if err := syscall.Kill(old, syscall.SIGKILL); err != nil {
		t.Fatalf("kill bank-a: %v", err)
	}
	waitFor(t, "bank-a to come back", func() bool {
		pid := processesIn(t, dir)["bank-a"]
		return pid != 0 && pid != old
	})
	p.want("the restarted bank to be suspended and down again", 503, 503, 503, "suspended")
	c.ok("bank", "resume", "bank-a")
	waitFor(t, "the restarted bank to answer the site but refuse NIP while the fault lasts", func() bool {
		return p.ui() == 200 && p.nip() == 503 && p.phase() == "active"
	})
	c.ok("fault", "clear")
	p.want("the restarted bank to open fully", 200, 200, 404, "active")
}
