package contracttest

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pb33f/libopenapi"
	validator "github.com/pb33f/libopenapi-validator"

	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

const (
	soloNIBSSWorld = "wld_solo"
	nipSpec        = "../api/nip/openapi.yaml"
	participantAPI = "../api/participant/openapi.yaml"
)

var requestIDText = regexp.MustCompile(`"request_id":"req_[A-Za-z0-9]+"`)

func specValidator(t *testing.T, path string) validator.Validator {
	t.Helper()
	spec, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := libopenapi.NewDocument(spec)
	if err != nil {
		t.Fatal(err)
	}
	v, errs := validator.NewValidator(doc)
	if len(errs) > 0 {
		t.Fatalf("validator %s: %v", path, errs)
	}
	return v
}

func nibssClient(t *testing.T, dir, certDir string) *http.Client {
	t.Helper()
	pair, err := tls.LoadX509KeyPair(filepath.Join(dir, certDir, "client.crt"), filepath.Join(dir, certDir, "client.key"))
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.ReadFile(filepath.Join(dir, "ca", "root.crt"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(root)
	tr := &http.Transport{TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{pair}, RootCAs: pool, ServerName: config.NIBSSHost, MinVersion: tls.VersionTLS12}, IdleConnTimeout: controlProbeTimeout, DisableKeepAlives: true}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: controlProbeTimeout}
}

type soloNIBSS struct {
	t       *testing.T
	dir     string
	control string
	token   string
	dirURL  string
	plain   *http.Client
	specs   map[string]validator.Validator
}

func startSoloNIBSS(t *testing.T) (*soloNIBSS, map[string]*http.Client) {
	t.Helper()
	if testing.Short() {
		t.Skip("starts a NIBSS process; run without -short")
	}
	ctx := context.WithoutCancel(t.Context())
	dbURL := cmp.Or(os.Getenv(config.DatabaseURLEnv), os.Getenv("BEERUS_TEST_DATABASE_URL"), "postgres:///postgres")
	now := clock.New(nil).Now()
	stage := filepath.Join(t.TempDir(), "stage")
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	ports := freePorts(t, 2)
	cfg := &config.Config{Dir: stage, Connect: config.Connect{Host: "connect.localhost", BrandName: "Connect", NIBSSPort: ports[0], NIBSSControlPort: ports[1]}}
	if err := config.Prepare(cfg, now); err != nil {
		t.Fatal(err)
	}
	if err := cfg.EnsureNIBSSDir(soloNIBSSWorld, now); err != nil {
		t.Fatal(err)
	}
	name := config.DatabaseName(stage, config.NIBSSID)
	if err := store.Create(ctx, dbURL, name); err != nil {
		t.Skipf("no Postgres for the solo NIBSS test (%v); set BEERUS_TEST_DATABASE_URL", err)
	}
	t.Cleanup(func() { _ = store.DropDatabases(ctx, dbURL, []string{name}) })
	clients := map[string]*http.Client{}
	clientDir := t.TempDir()
	copyBankFiles(t, stage, clientDir, "ca/root.crt")
	for _, id := range []string{"bank-a", "bank-z"} {
		b := &config.Bank{ID: id, Name: id, Type: "commercial", Port: 1, APIPort: 2, Database: config.DatabaseName(stage, id)}
		if err := cfg.EnsureBankDir(b, now); err != nil {
			t.Fatal(err)
		}
		copyBankFiles(t, stage, clientDir, "banks/"+id+"/client.crt", "banks/"+id+"/client.key")
		clients[id] = nibssClient(t, clientDir, filepath.Join("banks", id))
	}
	entries, err := os.ReadDir(stage)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != config.NIBSSID {
			if err := os.RemoveAll(filepath.Join(stage, e.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
	token, err := config.ReadSecret(filepath.Join(config.NIBSSDir(stage), config.NIBSSTokenFile))
	if err != nil {
		t.Fatal(err)
	}
	c := newCLIAt(t, stage)
	run := exec.Command(c.bin, "run", "--participant", config.NIBSSID, "--dir", stage)
	run.Env = c.env(config.DatabaseURLEnv + "=" + dbURL)
	logs := &lockedBuffer{}
	run.Stdout, run.Stderr = logs, logs
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- run.Wait() }()
	t.Cleanup(func() {
		_ = run.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(soloStop):
			_ = run.Process.Kill()
			<-exited
		}
	})
	s := &soloNIBSS{
		t:       t,
		dir:     stage,
		control: fmt.Sprintf("http://127.0.0.1:%d", ports[1]),
		token:   token,
		dirURL:  fmt.Sprintf("https://127.0.0.1:%d", ports[0]),
		plain:   &http.Client{Timeout: controlProbeTimeout},
		specs:   map[string]validator.Validator{"nip": specValidator(t, nipSpec), "participant": specValidator(t, participantAPI)},
	}
	waitFor(t, "NIBSS to answer on its own", func() bool {
		select {
		case err := <-exited:
			t.Fatalf("NIBSS exited: %v\n%s", err, logs.String())
		default:
		}
		code, _ := s.call("participant", s.plain, http.MethodGet, s.control+"/v1/health", s.token, "")
		return code == http.StatusOK
	})
	return s, clients
}

func (s *soloNIBSS) call(spec string, client *http.Client, method, url, token, body string) (int, string) {
	s.t.Helper()
	build := func() *http.Request {
		req, err := http.NewRequest(method, url, strings.NewReader(body))
		if err != nil {
			s.t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		return req
	}
	resp, err := client.Do(build())
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		s.t.Fatal(err)
	}
	if spec != "" {
		check := &http.Response{StatusCode: resp.StatusCode, Header: resp.Header, Body: io.NopCloser(bytes.NewReader(raw))}
		if ok, errs := s.specs[spec].ValidateHttpResponse(build(), check); !ok {
			s.t.Fatalf("%s %s answered %d %s, which breaks the %s contract: %v", method, url, resp.StatusCode, raw, spec, errs)
		}
	}
	return resp.StatusCode, requestIDText.ReplaceAllString(strings.TrimSpace(string(raw)), `"request_id":"req_ID"`)
}

func TestSoloNIBSS(t *testing.T) {
	s, banks := startSoloNIBSS(t)
	register := `{"nip_code":"000014","nuban_code":"014","name":"Bank A","type":"commercial","credit_url":"https://bank-a.localhost:7451","certificate_identity":"bank-a"}`
	moved := strings.Replace(register, "7451", "7499", 1)
	entry := func(status string) string {
		return fmt.Sprintf(`{"id":"bank-a","name":"Bank A","nip_code":"000014","nuban_code":"014","type":"commercial","status":%q}`, status)
	}
	errBody := func(code, message string) string {
		return fmt.Sprintf(`{"error":{"code":%q,"message":%q,"request_id":"req_ID"}}`, code, message)
	}
	bankURL := s.control + "/v1/control/banks/bank-a"
	rows := []struct {
		name   string
		client *http.Client
		spec   string
		method string
		url    string
		token  string
		body   string
		status int
		want   string
	}{
		{"register", s.plain, "nip", "PUT", bankURL, s.token, register, 200, entry("active")},
		{"register again", s.plain, "nip", "PUT", bankURL, s.token, register, 200, entry("active")},
		{"register with another url", s.plain, "nip", "PUT", bankURL, s.token, moved, 422, errBody("registration_conflict", "Bank bank-a is registered with credit_url https://bank-a.localhost:7451, and this request has credit_url https://bank-a.localhost:7499. Send the registered details.")},
		{"register with another nip code", s.plain, "nip", "PUT", bankURL, s.token, strings.Replace(register, "000014", "000015", 1), 422, errBody("registration_conflict", "Bank bank-a is registered with nip_code 000014, and this request has nip_code 000015. Send the registered details.")},
		{"register without a token", s.plain, "nip", "PUT", bankURL, "", register, 401, errBody("unauthenticated", "A valid control token is required.")},
		{"register with a wrong token", s.plain, "nip", "PUT", bankURL, "ctl_wrong", register, 401, errBody("unauthenticated", "A valid control token is required.")},
		{"register with no url", s.plain, "nip", "PUT", s.control + "/v1/control/banks/bank-y", s.token, strings.Replace(register, "https://bank-a.localhost:7451", "", 1), 400, errBody("invalid_request", "Send a credit_url that starts with http:// or https:// and a certificate_identity.")},
		{"register with a bad code", s.plain, "nip", "PUT", s.control + "/v1/control/banks/bank-y", s.token, strings.Replace(register, "000014", "14", 1), 400, errBody("invalid_request", `Bank bank-y: nip_code "14" must be 6 digits.`)},
		{"directory for a registered bank", banks["bank-a"], "nip", "GET", s.dirURL + "/v1/banks", "", "", 200, `{"data":[` + entry("active") + `]}`},
		{"directory for a stranger", banks["bank-z"], "nip", "GET", s.dirURL + "/v1/banks", "", "", 403, errBody("forbidden", "This certificate may not read the bank directory. bank-z is not a bank NIBSS knows.")},
		{"suspend", s.plain, "nip", "PUT", bankURL + "/status", s.token, `{"status":"suspended"}`, 200, entry("suspended")},
		{"directory shows it suspended", banks["bank-a"], "nip", "GET", s.dirURL + "/v1/banks", "", "", 200, `{"data":[` + entry("suspended") + `]}`},
		{"remove too early", s.plain, "nip", "PUT", bankURL + "/status", s.token, `{"status":"removed"}`, 422, errBody("status_not_allowed", "Bank bank-a is suspended and cannot become removed. It can become active or terminating.")},
		{"unknown status", s.plain, "nip", "PUT", bankURL + "/status", s.token, `{"status":"closed"}`, 400, errBody("invalid_request", "The status must be active, suspended, terminating or removed.")},
		{"unknown bank", s.plain, "nip", "PUT", s.control + "/v1/control/banks/bank-z/status", s.token, `{"status":"active"}`, 404, errBody("not_found", "Bank bank-z is not in the NIBSS directory. Register it first.")},
		{"status without a token", s.plain, "nip", "PUT", bankURL + "/status", "", `{"status":"active"}`, 401, errBody("unauthenticated", "A valid control token is required.")},
		{"terminate", s.plain, "nip", "PUT", bankURL + "/status", s.token, `{"status":"terminating"}`, 200, entry("terminating")},
		{"remove", s.plain, "nip", "PUT", bankURL + "/status", s.token, `{"status":"removed"}`, 200, entry("removed")},
		{"directory no longer admits it", banks["bank-a"], "nip", "GET", s.dirURL + "/v1/banks", "", "", 403, errBody("forbidden", "This certificate may not read the bank directory. bank-a is not a bank NIBSS knows.")},
		{"health", s.plain, "participant", "GET", s.control + "/v1/health", s.token, "", 200, `{"participant_id":"nibss","status":"ok","world_id":"` + soloNIBSSWorld + `"}`},
		{"status", s.plain, "participant", "GET", s.control + "/v1/status", s.token, "", 200, `{"participant_id":"nibss","kind":"nibss","phase":"active","generation":0,"observed_generation":0,"steps":[{"id":"database","name":"Making the NIBSS database","done":true},{"id":"schema","name":"Migrating the NIBSS database","done":true}]}`},
		{"unknown path", s.plain, "", "GET", s.control + "/v1/nothing", s.token, "", 404, errBody("not_found", "NIBSS does not serve this control call.")},
	}
	for _, r := range rows {
		status, got := s.call(r.spec, r.client, r.method, r.url, r.token, r.body)
		if status != r.status || got != r.want {
			t.Errorf("%s: got %d %s, want %d %s", r.name, status, got, r.status, r.want)
		}
	}
	files := []string{"nibss", "nibss/ca.crt", "nibss/client.crt", "nibss/client.key", "nibss/control-token", "nibss/nibss.json", "nibss/server.crt", "nibss/server.key"}
	if got := dirListing(t, s.dir); !slices.Equal(got, files) {
		t.Errorf("the NIBSS directory holds %v, want %v", got, files)
	}
	for _, f := range []string{"nibss.json", "control-token", "client.key", "server.key"} {
		fi, err := os.Stat(filepath.Join(config.NIBSSDir(s.dir), f))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: mode %v, error %v, want 0600", f, fi, err)
		}
	}
}
