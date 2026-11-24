package contracttest

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pb33f/libopenapi"
	validator "github.com/pb33f/libopenapi-validator"

	"github.com/evolutionlabs-core/beerus/internal/aggregator"
	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/banktest"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/core"
	"github.com/evolutionlabs-core/beerus/internal/countrypack"
	"github.com/evolutionlabs-core/beerus/internal/postgres"
	"github.com/evolutionlabs-core/beerus/internal/seed"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

const (
	healthyWithin  = 30 * time.Second
	readyPoll      = 20 * time.Millisecond
	readyStreak    = 3
	stackPortLow   = 20000
	stackPortHigh  = 32000
	stackAccessSec = 130
	startAttempts  = 5
)

type hook struct {
	Header http.Header
	Body   []byte
}

type sink struct {
	mu    sync.Mutex
	hooks []hook
	srv   *httptest.Server
}

func newSink() *sink {
	s := &sink{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.hooks = append(s.hooks, hook{Header: r.Header.Clone(), Body: b})
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	return s
}

func (s *sink) all() []hook {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]hook(nil), s.hooks...)
}

var spareBanks = []setup.Bank{
	{ID: "close-digital", Name: "Close Digital", Type: "digital"},
	{ID: "kano-microfinance", Name: "Kano Microfinance", Type: "microfinance", SettlesThrough: "bank-a"},
	{ID: "purge-digital", Name: "Purge Digital", Type: "digital"},
	{ID: "purge-leave", Name: "Purge Leave", Type: "digital"},
	{ID: "repair-digital", Name: "Repair Digital", Type: "digital"},
}

func localTransport() *http.Transport {
	d := &net.Dialer{}
	return &http.Transport{IdleConnTimeout: time.Minute, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(host, ".localhost") {
			host = "127.0.0.1"
		}
		return d.DialContext(ctx, network, net.JoinHostPort(host, port))
	}}
}

type env struct {
	cfg        *config.Config
	app        *core.App
	agg        *aggregator.App
	nodes      *nodes
	aggOpts    aggregator.Options
	api        *http.Client
	raw        *http.Client
	secret     string
	ctlToken   string
	sink       *sink
	external   bool
	redirect   string
	validator  validator.Validator
	vmu        sync.Mutex
	violations []string
	people     map[string]persona
	dir        string
}

type Stack struct {
	t    *testing.T
	flow string
	*env
}

var (
	sharedOnce sync.Once
	sharedEnv  *env
	sharedErr  error

	binOnce sync.Once
	binDir  string
	binErr  error
	binDone = make(chan builtResult, 1)
)

func TestMain(m *testing.M) {
	go buildBinary()
	code := m.Run()
	if sharedEnv != nil {
		if err := sharedEnv.stop(); err != nil {
			fmt.Fprintln(os.Stderr, "stop the shared stack:", err)
			code = 1
		}
	}
	if err := waitBinary(); err != nil && code == 0 {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	if binDir != "" {
		if err := os.RemoveAll(binDir); err != nil {
			fmt.Fprintln(os.Stderr, "remove the build directory:", err)
		}
	}
	os.Exit(code)
}

type builtResult struct {
	dir string
	err error
}

func buildBinary() {
	dir, err := os.MkdirTemp("", "beerus-bin-")
	if err != nil {
		binDone <- builtResult{err: fmt.Errorf("make build directory: %w", err)}
		return
	}
	_, file, _, _ := runtime.Caller(0)
	build := exec.Command("go", "build", "-o", filepath.Join(dir, "beerus"), "github.com/evolutionlabs-core/beerus/cmd/beerus")
	build.Dir = filepath.Dir(file)
	if out, err := build.CombinedOutput(); err != nil {
		binDone <- builtResult{dir: dir, err: fmt.Errorf("build beerus: %w\n%s", err, out)}
		return
	}
	binDone <- builtResult{dir: dir}
}

func waitBinary() error {
	binOnce.Do(func() {
		r := <-binDone
		binDir, binErr = r.dir, r.err
	})
	return binErr
}

func builtBinary(t *testing.T) string {
	t.Helper()
	if err := waitBinary(); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(binDir, "beerus")
}

func specPath() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "api", "partner", "openapi.yaml")
}

func freePorts(t *testing.T, n int) []int {
	ports := []int{}
	for i := 0; i < n; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		ports = append(ports, l.Addr().(*net.TCPAddr).Port)
	}
	return ports
}

func pickPorts(banks int) ([]int, error) {
	offset := config.DefaultAPIPortOffset
	need := 8 + banks
	for range 200 {
		base := stackPortLow + mrand.IntN(stackPortHigh-stackPortLow-need-offset)
		free := true
		for p := base; p < base+need && free; p++ {
			free = portOpen(p) && (p < base+8 || portOpen(p+offset))
		}
		if free {
			return []int{base, base + 1, base + 2, base + 3, base + 4, base + 5, base + 6, base + 7}, nil
		}
	}
	return nil, errors.New("no free block of ports for the stack")
}

func stackPorts(t *testing.T, banks int) []int {
	t.Helper()
	ports, err := pickPorts(banks)
	if err != nil {
		t.Fatal(err)
	}
	return ports
}

func portOpen(p int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

func initStack(dir, webhookURL string, spares ...setup.Bank) (*config.Config, error) {
	spec := banktest.Spec("contract")
	spec.Spec.Banks = append(spec.Spec.Banks, spares...)
	ports, err := pickPorts(len(spec.Spec.Banks))
	if err != nil {
		return nil, err
	}
	local := setup.DefaultLocal()
	if u := os.Getenv("BEERUS_TEST_DATABASE_URL"); u != "" {
		local.DatabaseURL = u
	}
	local.Connect.PartnerPort, local.Connect.WidgetPort, local.Connect.ControlPort, local.Connect.HealthPort, local.Connect.NIBSSPort, local.Connect.CBNPort, local.Connect.ConnectControlPort = ports[0], ports[1], ports[2], ports[3], ports[4], ports[5], ports[7]
	spec.Spec.NIP.Sessions = 1
	for i := range spec.Spec.Banks {
		spec.Spec.Banks[i].OpenBanking.AccessSeconds = stackAccessSec
	}
	spec.Spec.Partners = []setup.Partner{{
		ID:           "balance",
		Name:         "Balance",
		LegalName:    "Evolution Labs",
		Purpose:      "to keep your books in sync with this account",
		RedirectURIs: []string{"http://balance.localhost/bank_connections/callback"},
		WebhookURL:   webhookURL,
	}}
	if err := seed.Init(dir, spec, local, clock.New().Today()); err != nil {
		return nil, err
	}
	return setup.LoadConfig(dir)
}

var partnerValidator = sync.OnceValues(func() (validator.Validator, error) {
	spec, err := os.ReadFile(specPath())
	if err != nil {
		return nil, err
	}
	doc, err := libopenapi.NewDocument(spec)
	if err != nil {
		return nil, err
	}
	v, errs := validator.NewValidator(doc)
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return v, nil
})

func startEnv(opts aggregator.Options, spares []setup.Bank, edits ...func(*config.Config)) (*env, error) {
	e := &env{people: map[string]persona{}, aggOpts: opts}
	var err error
	if e.validator, err = partnerValidator(); err != nil {
		return nil, fmt.Errorf("load partner spec: %w", err)
	}
	if dir := os.Getenv("BEERUS_CONTRACT_DIR"); dir != "" {
		if e.cfg, err = setup.LoadConfig(dir); err != nil {
			return nil, err
		}
		e.external = true
	} else if err := e.startNodes(opts, spares, edits); err != nil {
		return nil, err
	}
	e.redirect = e.cfg.Partners[0].RedirectURIs[0]
	if e.secret, err = config.ReadSecret(e.cfg.Path("partners", "balance", config.PartnerSecretFile)); err != nil {
		return nil, errors.Join(err, e.stop())
	}
	if e.ctlToken, err = config.ReadSecret(e.cfg.Path("control-token")); err != nil {
		return nil, errors.Join(err, e.stop())
	}
	e.api = &http.Client{Timeout: 30 * time.Second, Transport: &validating{e: e, base: localTransport()}}
	e.raw = &http.Client{Timeout: 30 * time.Second, Transport: localTransport()}
	if err := e.waitReady(); err != nil {
		return nil, errors.Join(err, e.stop())
	}
	return e, nil
}

func (e *env) startNodes(opts aggregator.Options, spares []setup.Bank, edits []func(*config.Config)) error {
	e.sink = newSink()
	var lastErr error
	for range startAttempts {
		dir, err := os.MkdirTemp("", "beerus-contract-")
		if err != nil {
			return err
		}
		e.dir = dir
		cfg, err := initStack(dir, e.sink.srv.URL+"/webhooks", spares...)
		if err != nil {
			return errors.Join(err, os.RemoveAll(dir))
		}
		for _, edit := range edits {
			edit(cfg)
		}
		ns, err := startNodes(cfg, opts)
		if err != nil {
			lastErr = err
			err = errors.Join(dropDatabases(cfg), os.RemoveAll(dir))
			if err != nil {
				return err
			}
			continue
		}
		e.cfg, e.app, e.agg, e.nodes = cfg, ns.app, ns.agg, ns
		return nil
	}
	return fmt.Errorf("could not start the stack: %w", lastErr)
}

func (e *env) stop() error {
	var errs []error
	if e.nodes != nil {
		errs = append(errs, e.nodes.halt(), dropDatabases(e.cfg))
		e.nodes = nil
	}
	if e.sink != nil {
		e.sink.srv.Close()
	}
	if e.dir != "" {
		errs = append(errs, os.RemoveAll(e.dir))
	}
	return errors.Join(errs...)
}

func dropDatabases(cfg *config.Config) error {
	ctx := context.Background()
	names, err := postgres.List(ctx, cfg.DatabaseURL, config.DatabaseName(cfg.Dir, ""))
	if err != nil {
		return fmt.Errorf("drop databases: %w", err)
	}
	if err := postgres.DropDatabases(ctx, cfg.DatabaseURL, names); err != nil {
		return fmt.Errorf("drop databases: %w", err)
	}
	return nil
}

func (e *env) waitReady() error {
	ctx, cancel := context.WithTimeout(context.Background(), healthyWithin)
	defer cancel()
	tick := time.NewTicker(readyPoll)
	defer tick.Stop()
	streak := 0
	for {
		if e.app != nil {
			e.app.Step(ctx)
		}
		if e.ready() {
			streak++
		} else {
			streak = 0
		}
		if streak == readyStreak {
			return nil
		}
		select {
		case <-ctx.Done():
			banks, err := e.ctlTry("GET", "/banks", nil)
			return fmt.Errorf("stack did not become healthy within %s: %v %v", healthyWithin, banks, err)
		case <-tick.C:
		}
	}
}

func (e *env) ready() bool {
	if _, err := e.ctlTry("GET", "/health", nil); err != nil {
		return false
	}
	out, err := e.ctlTry("GET", "/banks", nil)
	if err != nil {
		return false
	}
	rows, _ := out["data"].([]any)
	if len(rows) == 0 {
		return false
	}
	for _, r := range rows {
		b, _ := r.(map[string]any)
		if b["phase"] != config.PhaseActive || b["attempts"] != nil {
			return false
		}
	}
	for _, b := range e.cfg.Banks {
		resp, err := e.raw.Get(b.URL() + "/login")
		if err != nil {
			return false
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return false
		}
	}
	out, err = e.ctlTry("GET", "/registry/clients", nil)
	if err != nil {
		return false
	}
	list, _ := out["data"].([]any)
	return len(list) > 0
}

func sharedStack(t *testing.T) *Stack {
	t.Helper()
	if testing.Short() {
		t.Skip("starts a full stack; run without -short")
	}
	sharedOnce.Do(func() { sharedEnv, sharedErr = startEnv(aggregator.Options{}, spareBanks) })
	if sharedErr != nil {
		t.Fatal(sharedErr)
	}
	return attach(t, sharedEnv)
}

func ownStack(t *testing.T, opts aggregator.Options, edits ...func(*config.Config)) *Stack {
	t.Helper()
	if testing.Short() {
		t.Skip("starts a full stack; run without -short")
	}
	e, err := startEnv(opts, nil, edits...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := e.stop(); err != nil {
			t.Errorf("stop the stack: %v", err)
		}
	})
	return attach(t, e)
}

func attach(t *testing.T, e *env) *Stack {
	t.Helper()
	top, _, _ := strings.Cut(t.Name(), "/")
	s := &Stack{t: t, flow: strings.ToLower(strings.TrimPrefix(top, "Test")), env: e}
	e.vmu.Lock()
	seen := len(e.violations)
	e.vmu.Unlock()
	t.Cleanup(func() {
		e.vmu.Lock()
		defer e.vmu.Unlock()
		for _, v := range e.violations[seen:] {
			t.Errorf("contract violation: %s", v)
		}
	})
	return s
}

func (s *Stack) needsNodes() {
	s.t.Helper()
	if s.external {
		s.t.Skip("needs an in-process stack")
	}
}

func (s *Stack) sub(t *testing.T) *Stack {
	c := *s
	c.t = t
	return &c
}

func (s *Stack) loadConfig() *config.Config {
	s.t.Helper()
	cfg, err := setup.LoadConfig(s.cfg.Dir)
	if err != nil {
		s.t.Fatal(err)
	}
	return cfg
}

func (s *Stack) owned(kind, id string) []setup.Owned {
	s.t.Helper()
	st, err := setup.LoadStatus(s.cfg.Dir)
	if err != nil {
		s.t.Fatal(err)
	}
	for _, r := range st.Resources {
		if r.Kind == kind && r.ID == id {
			return r.Owned
		}
	}
	return nil
}

type validating struct {
	e    *env
	base http.RoundTripper
}

func (v *validating) RoundTrip(req *http.Request) (*http.Response, error) {
	skip := req.Header.Get("X-Test-Skip-Validation") != ""
	req.Header.Del("X-Test-Skip-Validation")
	var reqBody []byte
	if req.Body != nil {
		reqBody, _ = io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
	}
	resp, err := v.base.RoundTrip(req)
	if err != nil || skip {
		return resp, err
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(respBody))
	check := req.Clone(context.Background())
	check.Body = io.NopCloser(bytes.NewReader(reqBody))
	vresp := *resp
	vresp.Body = io.NopCloser(bytes.NewReader(respBody))
	v.e.vmu.Lock()
	defer v.e.vmu.Unlock()
	if ok, errs := v.e.validator.ValidateHttpResponse(check, &vresp); !ok {
		for _, e := range errs {
			detail := []string{}
			for _, f := range e.SchemaValidationErrors {
				detail = append(detail, f.FieldPath+": "+f.Reason)
			}
			v.e.violations = append(v.e.violations, fmt.Sprintf("%s %s -> %d: %s %s %v", req.Method, req.URL.Path, resp.StatusCode, e.Message, e.Reason, detail))
		}
	}
	if resp.StatusCode < 300 {
		check2 := req.Clone(context.Background())
		check2.Body = io.NopCloser(bytes.NewReader(reqBody))
		if ok, errs := v.e.validator.ValidateHttpRequest(check2); !ok {
			for _, e := range errs {
				v.e.violations = append(v.e.violations, fmt.Sprintf("request %s %s: %s %s", req.Method, req.URL.Path, e.Message, e.Reason))
			}
		}
	}
	return resp, nil
}

type Resp struct {
	Status int
	Header http.Header
	Body   []byte
	JSON   map[string]any
}

func (r Resp) Code() string {
	if e, ok := r.JSON["error"].(map[string]any); ok {
		return fmt.Sprint(e["code"])
	}
	return ""
}

func randomKey() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type callOpt func(*http.Request)

func withKey(k string) callOpt {
	return func(r *http.Request) { r.Header.Set("Idempotency-Key", k) }
}

func withToken(token string) callOpt {
	return func(r *http.Request) { r.Header.Set(config.AccessTokenHeader, token) }
}

func noIdem() callOpt {
	return func(r *http.Request) { r.Header.Del("Idempotency-Key") }
}

func skipValidation() callOpt {
	return func(r *http.Request) { r.Header.Set("X-Test-Skip-Validation", "1") }
}

func credentials(clientID, secret string) callOpt {
	return func(r *http.Request) {
		r.Header.Del(config.ClientIDHeader)
		r.Header.Del(config.SecretHeader)
		if clientID != "" {
			r.Header.Set(config.ClientIDHeader, clientID)
		}
		if secret != "" {
			r.Header.Set(config.SecretHeader, secret)
		}
	}
}

func (s *Stack) call(method, path string, body any, opts ...callOpt) Resp {
	s.t.Helper()
	var rd io.Reader
	if body != nil {
		switch b := body.(type) {
		case []byte:
			rd = bytes.NewReader(b)
		default:
			j, _ := json.Marshal(body)
			rd = bytes.NewReader(j)
		}
	}
	req, err := http.NewRequest(method, s.cfg.PartnerURL()+path, rd)
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set(config.ClientIDHeader, "balance")
	req.Header.Set(config.SecretHeader, s.secret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method == http.MethodPost || method == http.MethodDelete {
		req.Header.Set("Idempotency-Key", randomKey())
	}
	for _, o := range opts {
		o(req)
	}
	resp, err := s.api.Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	r := Resp{Status: resp.StatusCode, Header: resp.Header, Body: b}
	if len(b) > 0 {
		_ = json.Unmarshal(b, &r.JSON)
	}
	return r
}

func (s *Stack) must(r Resp, status int) Resp {
	s.t.Helper()
	if r.Status != status {
		s.t.Fatalf("want HTTP %d, got %d: %s", status, r.Status, r.Body)
	}
	return r
}

func (s *env) ctlTry(method, path string, body any) (map[string]any, error) {
	out, _, err := s.ctlWith(method, path, body, nil)
	return out, err
}

func (s *env) ctlWith(method, path string, body any, header map[string]string) (map[string]any, *http.Response, error) {
	var rd io.Reader
	if body != nil {
		j, _ := json.Marshal(body)
		rd = bytes.NewReader(j)
	}
	req, _ := http.NewRequest(method, s.cfg.ControlURL()+path, rd)
	req.Header.Set("Authorization", "Bearer "+s.ctlToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		req.Header.Set("Idempotency-Key", randomKey())
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	out := map[string]any{}
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &out)
	if resp.StatusCode >= 300 {
		return out, resp, fmt.Errorf("control %s %s: HTTP %d %s", method, path, resp.StatusCode, b)
	}
	return out, resp, nil
}

func (s *Stack) ctl(method, path string, body any) map[string]any {
	s.t.Helper()
	out, err := s.ctlTry(method, path, body)
	if err != nil {
		s.t.Fatal(err)
	}
	return out
}

func (s *Stack) syncNow() {
	s.t.Helper()
	s.ctl("POST", "/sync", nil)
}

func (s *Stack) today() clock.Date {
	return clock.New().Today()
}

type persona struct {
	ID, Bank, Customer, Name, Username, Password, BVN string
	Accounts                                          []string
}

type opening struct {
	typ, naira string
}

var people = map[string]struct {
	name, phone, dob string
	banks            map[string][]opening
}{
	"adaeze": {"Adaeze's Bakery Ltd", "08031234567", "1988-03-14", map[string][]opening{"bank-a": {{"current", "1250000"}, {"savings", "3000000"}}, "bank-b": {{"current", "450000"}}}},
	"tunde":  {"Tunde Motors Ltd", "08032345678", "1979-07-02", map[string][]opening{"bank-a": {{"current", "4800000"}}}},
	"ngozi":  {"Ngozi Fabrics Ventures", "08033456789", "1991-11-23", map[string][]opening{"bank-b": {{"current", "320000"}}}},
	"chidi":  {"Chidi Okafor", "08034567890", "1995-05-30", map[string][]opening{"bank-b": {{"savings", "85000"}}}},
}

func (s *Stack) key(id, bank string) string {
	return s.flow + "/" + id + "@" + bank
}

func (s *Stack) home(id string) string {
	s.t.Helper()
	p, ok := people[id]
	if !ok {
		s.t.Fatalf("no test customer %s", id)
	}
	if _, ok := p.banks["bank-a"]; ok {
		return "bank-a"
	}
	return "bank-b"
}

func (s *Stack) persona(id string) persona {
	s.t.Helper()
	return s.personaAt(id, s.home(id))
}

func (s *Stack) personaAt(id, bank string) persona {
	s.t.Helper()
	if p, ok := s.people[s.key(id, bank)]; ok {
		return p
	}
	spec, ok := people[id]
	if !ok || spec.banks[bank] == nil {
		s.t.Fatalf("no test customer %s at %s", id, bank)
	}
	p := s.join(id, bank)
	for _, o := range spec.banks[bank] {
		nuban := s.openAccount(p, o.typ)
		s.ctl("POST", "/banks/"+bank+"/deposits", map[string]string{"account": nuban, "amount": o.naira})
	}
	return s.people[s.key(id, bank)]
}

func (s *Stack) join(id, bank string) persona {
	s.t.Helper()
	spec, ok := people[id]
	if !ok {
		s.t.Fatalf("no test customer %s", id)
	}
	p := persona{ID: id, Bank: bank, Name: spec.name, Username: id + "-" + s.flow, Password: "beerus-test-" + id}
	c := s.ctl("POST", "/banks/"+bank+"/customers", map[string]string{"name": spec.name, "phone": spec.phone, "dob": spec.dob, "username": p.Username, "password": p.Password})
	p.Customer, p.BVN = fmt.Sprint(c["id"]), fmt.Sprint(c["bvn"])
	s.people[s.key(id, bank)] = p
	return p
}

func (s *Stack) openAccount(p persona, typ string) string {
	s.t.Helper()
	acc := s.ctl("POST", "/banks/"+p.Bank+"/accounts", map[string]string{"customer": p.Customer, "type": typ})
	nuban := fmt.Sprint(acc["nuban"])
	p = s.people[s.key(p.ID, p.Bank)]
	p.Accounts = append(p.Accounts, nuban)
	s.people[s.key(p.ID, p.Bank)] = p
	return nuban
}

func (s *Stack) balances(p persona) map[string]int64 {
	s.t.Helper()
	out := map[string]int64{}
	for _, a := range s.ctl("GET", "/banks/"+p.Bank+"/accounts?customer="+p.Customer, nil)["data"].([]any) {
		m := a.(map[string]any)
		out[fmt.Sprint(m["nuban"])] = int64(m["balance_minor"].(float64))
	}
	return out
}

func (s *Stack) closedBalances(p persona) map[string]int64 {
	s.t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, s.cfg.DatabaseURL, s.cfg.Bank(p.Bank).Database)
	if err != nil {
		s.t.Fatal(err)
	}
	defer st.Close()
	l := ledger.New(st.Pool, nil)
	accounts, _, err := bank.New(l, bank.Profile{}, nil).Accounts(ctx, p.Customer, 100, "")
	if err != nil {
		s.t.Fatal(err)
	}
	out := map[string]int64{}
	for _, a := range accounts {
		if out[a.NUBAN], err = l.Balance(ctx, a.Code); err != nil {
			s.t.Fatal(err)
		}
	}
	return out
}

func (s *Stack) bankOf(host string) string {
	for _, b := range s.cfg.Banks {
		if mustHost(b.URL()) == host {
			return b.ID
		}
	}
	s.t.Fatalf("no bank serves %s", host)
	return ""
}

var (
	reCSRF    = regexp.MustCompile(`name="csrf" value="([^"]+)"`)
	reAccount = regexp.MustCompile(`name="account" value="([^"]+)"`)
)

type browser struct {
	s      *Stack
	c      *http.Client
	last   string
	result *url.URL
	cur    *url.URL
	picked func([]string) []string
}

func (s *Stack) browser() *browser {
	jar, _ := cookiejar.New(nil)
	b := &browser{s: s}
	pu, _ := url.Parse(s.redirect)
	partnerHost := pu.Host
	b.c = &http.Client{
		Jar:       jar,
		Timeout:   30 * time.Second,
		Transport: s.raw.Transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Host == partnerHost {
				b.result = req.URL
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	return b
}

func (b *browser) get(u string) int {
	b.s.t.Helper()
	resp, err := b.c.Get(u)
	if err != nil {
		b.s.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	b.last = string(body)
	b.cur = resp.Request.URL
	return resp.StatusCode
}

func (b *browser) post(path string, form url.Values) int {
	b.s.t.Helper()
	return b.postTo(b.s.cfg.WidgetURL(), path, form)
}

func (b *browser) postTo(base, path string, form url.Values) int {
	b.s.t.Helper()
	if m := reCSRF.FindStringSubmatch(b.last); m != nil && form.Get("csrf") == "" {
		form.Set("csrf", m[1])
	}
	req, _ := http.NewRequest("POST", base+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", base)
	resp, err := b.c.Do(req)
	if err != nil {
		b.s.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	b.last = string(body)
	b.cur = resp.Request.URL
	return resp.StatusCode
}

func (b *browser) atBank() bool {
	return b.cur != nil && b.cur.Host != "" && b.cur.Host != mustHost(b.s.cfg.WidgetURL())
}

func mustHost(raw string) string {
	u, _ := url.Parse(raw)
	return u.Host
}

func (b *browser) bankSignIn(personaID string, accounts func([]string) []string) {
	b.s.t.Helper()
	base := b.cur.Scheme + "://" + b.cur.Host
	p := b.s.personaAt(personaID, b.s.bankOf(b.cur.Host))
	b.postTo(base, "/login", url.Values{"username": {p.Username}, "password": {p.Password}})
	if b.result != nil {
		return
	}
	all := []string{}
	for _, m := range reAccount.FindAllStringSubmatch(b.last, -1) {
		all = append(all, m[1])
	}
	if len(all) == 0 {
		b.s.t.Fatalf("no accounts on the bank's consent page: %.400s", b.last)
	}
	pick := all
	if accounts != nil {
		pick = accounts(all)
	}
	b.postTo(base, "/authorize/consent", url.Values{"account": pick, "action": {"approve"}})
	if b.result == nil {
		b.pickAccounts()
	}
}

func (b *browser) pickAccounts() {
	b.s.t.Helper()
	all := []string{}
	for _, m := range reAccount.FindAllStringSubmatch(b.last, -1) {
		all = append(all, m[1])
	}
	if len(all) == 0 {
		b.s.t.Fatalf("no accounts on the Pick accounts screen: %.400s", b.last)
	}
	pick := all
	if b.picked != nil {
		pick = b.picked(all)
	}
	b.post("/link/accounts", url.Values{"account": pick})
}

type linkResult struct {
	PublicToken, SessionID, State, Error string
}

func (s *Stack) completeLink(widgetURL, institution, personaID string, accounts func([]string) []string) linkResult {
	s.t.Helper()
	b := s.browser()
	b.get(widgetURL)
	if b.result == nil && institution != "" && strings.Contains(b.last, `name="institution"`) {
		b.post("/link/institution", url.Values{"institution": {institution}})
	}
	if b.result == nil && b.atBank() {
		b.bankSignIn(personaID, accounts)
	}
	if b.result == nil {
		s.t.Fatalf("widget did not redirect to the partner: %.400s", b.last)
	}
	q := b.result.Query()
	return linkResult{PublicToken: q.Get("public_token"), SessionID: q.Get("link_session_id"), State: q.Get("state"), Error: q.Get("error")}
}

type connected struct {
	ConnectionID string
	AccessToken  string
	Accounts     []map[string]any
	StartDate    clock.Date
}

func (s *Stack) linkStart() clock.Date {
	s.t.Helper()
	pack, err := countrypack.Nigeria()
	if err != nil {
		s.t.Fatal(err)
	}
	return s.today().AddDays(-pack.Link.HistoryDays)
}

func (s *Stack) linkURL(userID, state string) string {
	s.t.Helper()
	lt := s.must(s.call("POST", "/v1/link/token/create", map[string]any{"user_id": userID, "redirect_uri": s.redirect, "state": state}), 201)
	return s.cfg.WidgetURL() + "/link?token=" + fmt.Sprint(lt.JSON["link_token"])
}

func (s *Stack) connect(personaID, institution string) connected {
	s.t.Helper()
	res := s.completeLink(s.linkURL("ws_"+personaID, "st-"+personaID), institution, personaID, nil)
	if res.Error != "" || res.PublicToken == "" {
		s.t.Fatalf("link failed: %+v", res)
	}
	if res.SessionID == "" || res.State != "st-"+personaID {
		s.t.Fatalf("redirect carried the wrong session or state: %+v", res)
	}
	conn := s.must(s.call("POST", "/v1/public_token/exchange", map[string]any{"public_token": res.PublicToken}), 200)
	id := fmt.Sprint(conn.JSON["connection_id"])
	token := fmt.Sprint(conn.JSON["access_token"])
	s.syncNow()
	got := s.must(s.call("GET", "/v1/connection", nil, withToken(token)), 200)
	if got.JSON["status"] != "active" {
		s.t.Fatalf("connection not active after sync: %s", got.Body)
	}
	accts := s.must(s.call("GET", "/v1/accounts", nil, withToken(token)), 200)
	out := connected{ConnectionID: id, AccessToken: token, StartDate: s.linkStart()}
	for _, a := range accts.JSON["data"].([]any) {
		out.Accounts = append(out.Accounts, a.(map[string]any))
	}
	return out
}

type feedState struct {
	lines    map[string]map[string]any
	removed  map[string]bool
	cursor   string
	worldID  string
	modified []map[string]any
	pages    int
}

func (s *Stack) syncAll(token, accountID, cursor string, limit int) (*feedState, Resp) {
	s.t.Helper()
	st := &feedState{lines: map[string]map[string]any{}, removed: map[string]bool{}, cursor: cursor}
	return s.syncMore(token, accountID, st, limit)
}

func (s *Stack) syncMore(token, accountID string, st *feedState, limit int) (*feedState, Resp) {
	s.t.Helper()
	for i := 0; i < 10000; i++ {
		q := url.Values{"limit": {strconv.Itoa(limit)}}
		if st.cursor != "" {
			q.Set("cursor", st.cursor)
		}
		r := s.call("GET", "/v1/accounts/"+accountID+"/transactions/sync?"+q.Encode(), nil, withToken(token))
		if r.Status != 200 {
			return st, r
		}
		st.pages++
		for _, x := range r.JSON["added"].([]any) {
			m := x.(map[string]any)
			s.checkDates(m)
			st.lines[fmt.Sprint(m["id"])] = m
		}
		for _, x := range r.JSON["modified"].([]any) {
			m := x.(map[string]any)
			s.checkDates(m)
			st.lines[fmt.Sprint(m["id"])] = m
			st.modified = append(st.modified, m)
		}
		for _, x := range r.JSON["removed"].([]any) {
			id := fmt.Sprint(x.(map[string]any)["id"])
			if l, ok := st.lines[id]; ok && l["status"] != "pending" {
				s.t.Fatalf("a posted line was removed: %s", id)
			}
			delete(st.lines, id)
			st.removed[id] = true
		}
		st.cursor = fmt.Sprint(r.JSON["next_cursor"])
		st.worldID = fmt.Sprint(r.JSON["world_id"])
		if r.JSON["has_more"] != true {
			return st, r
		}
	}
	s.t.Fatal("sync did not finish")
	return st, Resp{}
}

func num(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	}
	return 0
}

func verifyV1a(pub ed25519.PublicKey, h hook, now time.Time) error {
	id := h.Header.Get("webhook-id")
	tsHeader := h.Header.Get("webhook-timestamp")
	sec, err := strconv.ParseInt(tsHeader, 10, 64)
	if err != nil {
		return fmt.Errorf("bad timestamp %q", tsHeader)
	}
	if d := now.Sub(time.Unix(sec, 0)); d > 5*time.Minute || d < -5*time.Minute {
		return fmt.Errorf("timestamp outside tolerance")
	}
	msg := append([]byte(id+"."+tsHeader+"."), h.Body...)
	for _, sig := range strings.Fields(h.Header.Get("webhook-signature")) {
		ver, val, ok := strings.Cut(sig, ",")
		if !ok || ver != "v1a" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(val)
		if err == nil && ed25519.Verify(pub, msg, raw) {
			return nil
		}
	}
	return fmt.Errorf("no valid v1a signature")
}

func (s *Stack) publicKey() ed25519.PublicKey {
	r := s.must(s.call("GET", "/v1/webhook_keys", nil), 200)
	k := r.JSON["data"].([]any)[0].(map[string]any)["public_key"].(string)
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(k, "whpk_"))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		s.t.Fatalf("bad public key %q", k)
	}
	return ed25519.PublicKey(raw)
}

func (s *Stack) events() []map[string]any {
	data := []any{}
	path := "/webhooks/deliveries?limit=500"
	for {
		out := s.ctl("GET", path, nil)
		data = append(data, out["data"].([]any)...)
		next, _ := out["next_cursor"].(string)
		if next == "" {
			break
		}
		path = "/webhooks/deliveries?limit=500&cursor=" + next
	}
	evs := []map[string]any{}
	for i := len(data) - 1; i >= 0; i-- {
		m := data[i].(map[string]any)
		body := map[string]any{}
		switch b := m["body"].(type) {
		case map[string]any:
			body = b
		case string:
			_ = json.Unmarshal([]byte(b), &body)
		}
		evs = append(evs, body)
	}
	return evs
}

func (s *Stack) eventsOf(typ string, match func(map[string]any) bool) []map[string]any {
	out := []map[string]any{}
	for _, e := range s.events() {
		if e["type"] != typ {
			continue
		}
		d, _ := e["data"].(map[string]any)
		if match == nil || match(d) {
			out = append(out, d)
		}
	}
	return out
}

func (s *Stack) firstDate(accountID string, start clock.Date) clock.Date {
	s.t.Helper()
	evs := s.eventsOf("historical_sync.completed", func(d map[string]any) bool { return d["account_id"] == accountID })
	if len(evs) == 0 {
		s.t.Fatalf("no historical_sync.completed for %s", accountID)
	}
	if eff, ok := evs[len(evs)-1]["effective_start_date"].(string); ok {
		d, err := clock.ParseDate(eff)
		if err != nil {
			s.t.Fatal(err)
		}
		return d
	}
	return start
}

func (s *Stack) checkDates(m map[string]any) {
	s.t.Helper()
	vd, err := time.Parse("2006-01-02", fmt.Sprint(m["value_date"]))
	if err != nil || vd.Year() < 2000 {
		s.t.Fatalf("line %v has value_date %v", m["id"], m["value_date"])
	}
	if b, ok := m["booked_at"].(string); ok {
		if bt, err := time.Parse(time.RFC3339, b); err != nil || bt.Year() < 2000 {
			s.t.Fatalf("line %v has booked_at %v", m["id"], b)
		}
	}
}

func (s *Stack) payTry(fromBank, from, toBank, to, naira string, header map[string]string) (map[string]any, error) {
	s.t.Helper()
	out, _, err := s.ctlWith("POST", "/banks/"+fromBank+"/transfers", map[string]string{"from": from, "to_bank": toBank, "to": to, "amount": naira}, header)
	return out, err
}

func (s *Stack) pay(fromBank, from, toBank, to, naira string) map[string]any {
	s.t.Helper()
	out, err := s.payTry(fromBank, from, toBank, to, naira, nil)
	if err != nil {
		s.t.Fatal(err)
	}
	return out
}

func (s *Stack) spend(p persona, n int) {
	s.t.Helper()
	to := s.persona("ngozi")
	if p.Bank == to.Bank {
		to = s.persona("tunde")
	}
	for range n {
		s.pay(p.Bank, p.Accounts[0], to.Bank, to.Accounts[0], "10000")
	}
}

type callerTransport struct {
	id   string
	base *http.Transport
}

func (c callerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set(config.CallerHeader, c.id)
	return c.base.RoundTrip(r)
}

func callerClient(t *testing.T, id string) *http.Client {
	t.Helper()
	base := localTransport()
	t.Cleanup(base.CloseIdleConnections)
	return &http.Client{Timeout: 30 * time.Second, Transport: callerTransport{id: id, base: base}}
}
