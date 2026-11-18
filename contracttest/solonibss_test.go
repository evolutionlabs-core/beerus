package contracttest

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/postgres"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pb33f/libopenapi"
	validator "github.com/pb33f/libopenapi-validator"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

const (
	soloSetupKey   = 1<<63 + 5
	soloNIBSSWorld = "wld_solo"
	nipSpec        = "../api/nip/openapi.yaml"
	participantAPI = "../api/participant/openapi.yaml"
)

var soloBanks = map[string]struct{ name, code, nuban string }{
	"bank-a": {"Bank A", "000014", "014"},
	"bank-b": {"Bank B", "000015", "015"},
}

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

type soloMember struct {
	bank  *config.Bank
	nuban string
	stop  func()
	logs  *lockedBuffer
	books *bank.Bank
}

type soloNIBSS struct {
	t       *testing.T
	dir     string
	dbURL   string
	control string
	token   string
	dirURL  string
	plain   *http.Client
	specs   map[string]validator.Validator
	members map[string]*soloMember
	exited  <-chan error
	logs    *lockedBuffer
}

const soloTSQAfter = time.Minute

func soloBankConfig(stage, id, name, code, nuban string, ports []int) *config.Bank {
	return &config.Bank{ID: id, Name: name, Type: "commercial", NIPCode: code, NUBANCode: nuban, HistoryDays: 730, Port: ports[0], APIPort: ports[1], Database: config.DatabaseName(stage, id)}
}

func startSoloNIBSS(t *testing.T, tsqAfter time.Duration, withBanks ...string) (*soloNIBSS, map[string]*http.Client) {
	t.Helper()
	if testing.Short() {
		t.Skip("starts a NIBSS process; run without -short")
	}
	ctx := context.WithoutCancel(t.Context())
	dbURL := cmp.Or(os.Getenv(config.DatabaseURLEnv), os.Getenv("BEERUS_TEST_DATABASE_URL"), "postgres:///postgres")
	now := clock.New().Now()
	stage := filepath.Join(t.TempDir(), "stage")
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	ports := freePorts(t, 3+2*len(withBanks))
	cfg := &config.Config{Dir: stage, SetupKey: soloSetupKey, TSQAfter: tsqAfter, Connect: config.Connect{Host: "connect.localhost", BrandName: "Connect", NIBSSPort: ports[0], NIBSSControlPort: ports[1], CBNPort: ports[len(ports)-1]}}
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
	t.Cleanup(func() { _ = postgres.DropDatabases(ctx, dbURL, []string{name}) })
	clients := map[string]*http.Client{}
	clientDir := t.TempDir()
	copyBankFiles(t, stage, clientDir, "ca/root.crt")
	members := map[string]*soloMember{}
	for _, id := range []string{"bank-a", "bank-b", "bank-z"} {
		i := slices.Index(withBanks, id)
		b := &config.Bank{ID: id, Name: id, Type: "commercial", Port: 1, APIPort: 2, Database: config.DatabaseName(stage, id)}
		if i >= 0 {
			b = soloBankConfig(stage, id, soloBanks[id].name, soloBanks[id].code, soloBanks[id].nuban, ports[2+2*i:4+2*i])
		}
		if err := cfg.EnsureBankDir(b, now); err != nil {
			t.Fatal(err)
		}
		copyBankFiles(t, stage, clientDir, "banks/"+id+"/client.crt", "banks/"+id+"/client.key")
		clients[id] = nibssClient(t, clientDir, filepath.Join("banks", id))
		if i >= 0 {
			books, account := seedSoloBank(t, ctx, dbURL, stage, b, now)
			members[id] = &soloMember{bank: b, nuban: account.NUBAN, books: books}
		}
	}
	entries, err := os.ReadDir(stage)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != config.NIBSSID && !(len(withBanks) > 0 && e.Name() == "banks") {
			if err := os.RemoveAll(filepath.Join(stage, e.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
	token, err := config.ReadSecret(filepath.Join(config.NIBSSDir(stage), config.NIBSSTokenFile))
	if err != nil {
		t.Fatal(err)
	}
	logs, exited, _ := runSolo(t, config.NIBSSID, stage, dbURL)
	s := &soloNIBSS{
		t:       t,
		dir:     stage,
		dbURL:   dbURL,
		control: fmt.Sprintf("http://127.0.0.1:%d", ports[1]),
		token:   token,
		dirURL:  fmt.Sprintf("https://127.0.0.1:%d", ports[0]),
		plain:   &http.Client{Timeout: controlProbeTimeout},
		specs:   map[string]validator.Validator{"nip": specValidator(t, nipSpec), "participant": specValidator(t, participantAPI)},
		members: members,
		exited:  exited,
		logs:    logs,
	}
	waitFor(t, waitLimit, "NIBSS to answer on its own", func() bool {
		select {
		case err := <-exited:
			t.Fatalf("NIBSS exited: %v\n%s", err, logs.String())
		default:
		}
		code, _ := s.call("participant", s.plain, http.MethodGet, s.control+"/v1/health", s.token, "")
		return code == http.StatusOK
	})
	for _, id := range withBanks {
		s.startBank(id)
	}
	return s, clients
}

func (s *soloNIBSS) startBank(id string) {
	s.t.Helper()
	m := s.members[id]
	logs, exited, stop := runSolo(s.t, id, s.dir, s.dbURL)
	m.logs, m.stop = logs, stop
	waitFor(s.t, waitLimit, id+" to answer on its own", func() bool {
		select {
		case err := <-exited:
			s.t.Fatalf("%s exited: %v\n%s", id, err, logs.String())
		default:
		}
		code, _ := s.call("", s.plain, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/health", m.bank.Port), "", "")
		return code == http.StatusOK
	})
}

func (s *soloNIBSS) call(spec string, client *http.Client, method, url, token, body string) (int, string) {
	s.t.Helper()
	status, _, got := s.callWith(spec, client, method, url, token, body, nil)
	return status, got
}

func (s *soloNIBSS) callWith(spec string, client *http.Client, method, url, token, body string, header http.Header) (int, http.Header, string) {
	s.t.Helper()
	build := func() *http.Request {
		req, err := http.NewRequest(method, url, strings.NewReader(body))
		if err != nil {
			s.t.Fatal(err)
		}
		for k, v := range header {
			req.Header[k] = v
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
		return 0, nil, err.Error()
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
	return resp.StatusCode, resp.Header, requestIDText.ReplaceAllString(strings.TrimSpace(string(raw)), `"request_id":"req_ID"`)
}

func TestSoloNIBSS(t *testing.T) {
	s, banks := startSoloNIBSS(t, soloTSQAfter)
	register := `{"nip_code":"000014","nuban_code":"014","name":"Bank A","type":"commercial","credit_url":"https://bank-a.localhost:17451","certificate_identity":"bank-a","net_debit_cap_minor":500000000,"collateral_minor":0}`
	moved := strings.Replace(register, "17451", "17499", 1)
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
		{"register without a cap", s.plain, "nip", "PUT", s.control + "/v1/control/banks/bank-y", s.token, strings.Replace(register, `,"net_debit_cap_minor":500000000`, "", 1), 400, errBody("invalid_request", "Send the net_debit_cap_minor and the collateral_minor of the bank, in kobo.")},
		{"register with a new cap", s.plain, "nip", "PUT", bankURL, s.token, strings.Replace(register, "500000000", "900000000", 1), 200, entry("active")},
		{"register again", s.plain, "nip", "PUT", bankURL, s.token, register, 200, entry("active")},
		{"register with another url", s.plain, "nip", "PUT", bankURL, s.token, moved, 422, errBody("registration_conflict", "Bank bank-a is registered with credit_url https://bank-a.localhost:17451, and this request has credit_url https://bank-a.localhost:17499. Send the registered details.")},
		{"register with another nip code", s.plain, "nip", "PUT", bankURL, s.token, strings.Replace(register, "000014", "000015", 1), 422, errBody("registration_conflict", "Bank bank-a is registered with nip_code 000014, and this request has nip_code 000015. Send the registered details.")},
		{"register without a token", s.plain, "nip", "PUT", bankURL, "", register, 401, errBody("unauthenticated", "A valid control token is required.")},
		{"register with a wrong token", s.plain, "nip", "PUT", bankURL, "ctl_wrong", register, 401, errBody("unauthenticated", "A valid control token is required.")},
		{"register with no url", s.plain, "nip", "PUT", s.control + "/v1/control/banks/bank-y", s.token, strings.Replace(register, "https://bank-a.localhost:17451", "", 1), 400, errBody("invalid_request", "Send a credit_url that starts with http:// or https:// and a certificate_identity.")},
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
		{"status", s.plain, "participant", "GET", s.control + "/v1/status", s.token, "", 200, `{"participant_id":"nibss","kind":"nibss","phase":"active","generation":0,"observed_generation":0,"steps":[{"id":"database","name":"Making the NIBSS database","done":true},{"id":"schema","name":"Migrating the NIBSS database","done":true}],"problems":[],"posted_sessions":0,"next_cutoff_at":"<cutoff>"}`},
		{"unknown path", s.plain, "", "GET", s.control + "/v1/nothing", s.token, "", 404, errBody("not_found", "NIBSS does not serve this control call.")},
	}
	cutoff := regexp.MustCompile(`"next_cutoff_at":"([^"]*)"`)
	for _, r := range rows {
		status, got := s.call(r.spec, r.client, r.method, r.url, r.token, r.body)
		if m := cutoff.FindStringSubmatch(got); m != nil {
			next, err := time.Parse(time.RFC3339, m[1])
			if err != nil || !next.After(time.Now()) || !strings.HasSuffix(m[1], "+01:00") {
				t.Errorf("%s: next_cutoff_at %q is not a future Lagos time: %v", r.name, m[1], err)
			}
			got = strings.Replace(got, m[1], "<cutoff>", 1)
		}
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

func TestSoloNIBSSNames(t *testing.T) {
	s, banks := startSoloNIBSS(t, soloTSQAfter, "bank-a")
	a := s.members["bank-a"]
	register := fmt.Sprintf(`{"nip_code":"000014","nuban_code":"014","name":"Bank A","type":"commercial","credit_url":"https://bank-a.localhost:%d","certificate_identity":"bank-a","net_debit_cap_minor":500000000,"collateral_minor":0}`, a.bank.APIPort)
	bankURL := s.control + "/v1/control/banks/bank-a"
	if status, got := s.call("nip", s.plain, "PUT", bankURL, s.token, register); status != 200 {
		t.Fatalf("register: %d %s", status, got)
	}
	errBody := func(code, message string) string {
		return fmt.Sprintf(`{"error":{"code":%q,"message":%q,"request_id":"req_ID"}}`, code, message)
	}
	answer := func(code, message, name, bankName, nuban string) string {
		out := fmt.Sprintf(`{"response_code":%q,"message":%q,"bank_code":"000014"`, code, message)
		if bankName != "" {
			out += fmt.Sprintf(`,"bank_name":%q`, bankName)
		}
		out += fmt.Sprintf(`,"account_number":%q`, nuban)
		if name != "" {
			out += fmt.Sprintf(`,"account_name":%q`, name)
		}
		return out + "}"
	}
	enquiry := func(code, nuban string) string {
		return s.dirURL + "/v1/name-enquiries?bank_code=" + code + "&account_number=" + nuban
	}
	unknownNUBAN := world.NUBAN("014", 99)
	badDigit := a.nuban[:9] + string('0'+(a.nuban[9]-'0'+1)%10)
	person := `{"name":"Ada Obi","phone":"08031234567","dob":"1990-04-21"}`
	other := `{"name":"Tunde Bello","phone":"08031234567","dob":"1990-04-21"}`
	suspend := func(status string) func() {
		return func() { s.call("nip", s.plain, "PUT", bankURL+"/status", s.token, `{"status":"`+status+`"}`) }
	}
	impostor := func() {
		body := strings.NewReplacer("000014", "000015", `"014"`, `"015"`, "Bank A", "Bank Z", `"bank-a"`, `"bank-z"`).Replace(register)
		s.call("nip", s.plain, "PUT", s.control+"/v1/control/banks/bank-z", s.token, body)
	}
	impostorNUBAN := world.NUBAN("015", 1)
	rows := []struct {
		name   string
		client *http.Client
		method string
		url    string
		body   string
		before func()
		status int
		want   string
	}{
		{"real name", banks["bank-a"], "GET", enquiry("000014", a.nuban), "", nil, 200, answer("00", "Successful.", "ADA OBI", "Bank A", a.nuban)},
		{"bad check digit", banks["bank-a"], "GET", enquiry("000014", badDigit), "", nil, 200, answer("07", "That is not a valid account number at Bank A.", "", "Bank A", badDigit)},
		{"short number", banks["bank-a"], "GET", enquiry("000014", "123456789"), "", nil, 400, errBody("invalid_request", "account_number must have 10 digits.")},
		{"short bank code", banks["bank-a"], "GET", enquiry("14", a.nuban), "", nil, 400, errBody("invalid_request", "bank_code must have 6 digits.")},
		{"unknown account", banks["bank-a"], "GET", enquiry("000014", unknownNUBAN), "", nil, 200, answer("25", "No such account at Bank A.", "", "Bank A", unknownNUBAN)},
		{"unknown bank code", banks["bank-a"], "GET", enquiry("000111", a.nuban), "", nil, 200, `{"response_code":"96","message":"Bank code 000111 is not in the NIBSS directory.","bank_code":"000111","account_number":"` + a.nuban + `"}`},
		{"no account number", banks["bank-a"], "GET", s.dirURL + "/v1/name-enquiries?bank_code=000014", "", nil, 400, errBody("invalid_request", "account_number must have 10 digits.")},
		{"stranger", banks["bank-z"], "GET", enquiry("000014", a.nuban), "", nil, 403, errBody("forbidden", "This certificate may not ask for account names. bank-z is not a bank NIBSS knows.")},
		{"suspended bank", banks["bank-a"], "GET", enquiry("000014", a.nuban), "", suspend("suspended"), 200, answer("91", "Bank A is not available right now.", "", "Bank A", a.nuban)},
		{"resumed bank", banks["bank-a"], "GET", enquiry("000014", a.nuban), "", suspend("active"), 200, answer("00", "Successful.", "ADA OBI", "Bank A", a.nuban)},
		{"enrol a BVN", banks["bank-a"], "POST", s.dirURL + "/v1/bvns", person, nil, 200, `{"bvn":"22900000013","phone":"2348031234567"}`},
		{"enrol the same person", banks["bank-a"], "POST", s.dirURL + "/v1/bvns", person, nil, 200, `{"bvn":"22900000013","phone":"2348031234567"}`},
		{"enrol another person", banks["bank-a"], "POST", s.dirURL + "/v1/bvns", other, nil, 200, `{"bvn":"22900000021","phone":"2348031234567"}`},
		{"enrol with a bad phone", banks["bank-a"], "POST", s.dirURL + "/v1/bvns", `{"name":"Ada Obi","phone":"12345","dob":"1990-04-21"}`, nil, 422, errBody("invalid_request", `Phone "12345" must start with +234 or 0.`)},
		{"enrol with a bad date", banks["bank-a"], "POST", s.dirURL + "/v1/bvns", `{"name":"Ada Obi","phone":"08031234567","dob":"1990-02-30"}`, nil, 422, errBody("invalid_request", `Dob "1990-02-30" must be a date like 1990-04-21.`)},
		{"enrol with bad JSON", banks["bank-a"], "POST", s.dirURL + "/v1/bvns", `{"name":`, nil, 400, errBody("invalid_request", `Send {"name":"Ada Obi","phone":"08031234567","dob":"1990-04-21"}.`)},
		{"enrol as a stranger", banks["bank-z"], "POST", s.dirURL + "/v1/bvns", person, nil, 403, errBody("forbidden", "This certificate may not enrol a BVN. bank-z is not a bank NIBSS knows.")},
		{"bank behind another certificate", banks["bank-a"], "GET", enquiry("000015", impostorNUBAN), "", impostor, 200, `{"response_code":"91","message":"Bank Z is not available right now.","bank_code":"000015","bank_name":"Bank Z","account_number":"` + impostorNUBAN + `"}`},
		{"bank stopped", banks["bank-a"], "GET", enquiry("000014", a.nuban), "", a.stop, 200, answer("91", "Bank A is not available right now.", "", "Bank A", a.nuban)},
	}
	control := []struct {
		name   string
		url    string
		token  string
		status int
		want   string
	}{
		{"control name", s.control + "/v1/control/name-enquiries?bank_code=000014&account_number=" + a.nuban, s.token, 200, answer("00", "Successful.", "ADA OBI", "Bank A", a.nuban)},
		{"control without a token", s.control + "/v1/control/name-enquiries?bank_code=000014&account_number=" + a.nuban, "", 401, errBody("unauthenticated", "A valid control token is required.")},
		{"control with a wrong token", s.control + "/v1/control/name-enquiries?bank_code=000014&account_number=" + a.nuban, "ctl_wrong", 401, errBody("unauthenticated", "A valid control token is required.")},
		{"control with a short number", s.control + "/v1/control/name-enquiries?bank_code=000014&account_number=123", s.token, 400, errBody("invalid_request", "account_number must have 10 digits.")},
	}
	for _, r := range control {
		status, got := s.call("nip", s.plain, "GET", r.url, r.token, "")
		if status != r.status || got != r.want {
			t.Errorf("%s: got %d %s, want %d %s", r.name, status, got, r.status, r.want)
		}
	}
	for _, r := range rows {
		if r.before != nil {
			r.before()
		}
		status, got := s.call("nip", r.client, r.method, r.url, "", r.body)
		if status != r.status || got != r.want {
			t.Errorf("%s: got %d %s, want %d %s", r.name, status, got, r.status, r.want)
		}
	}
}
