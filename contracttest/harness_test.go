package contracttest

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
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

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/connect"
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

type Stack struct {
	t          *testing.T
	cfg        *config.Config
	app        *connect.App
	api        *http.Client
	raw        *http.Client
	apiKey     string
	ctlToken   string
	sink       *sink
	external   bool
	redirect   string
	validator  validator.Validator
	vmu        sync.Mutex
	violations []string
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

func newStack(t *testing.T) *Stack {
	t.Helper()
	s := &Stack{t: t}
	if dir := os.Getenv("BEERUS_CONTRACT_DIR"); dir != "" {
		cfg, err := config.Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		s.cfg = cfg
		s.external = true
	} else {
		s.sink = newSink()
		t.Cleanup(s.sink.srv.Close)
		var lastErr error
		for attempt := 0; attempt < 5 && s.app == nil; attempt++ {
			ports := freePorts(t, 3)
			cfg, err := config.Init(t.TempDir(), config.InitOptions{
				Seed:        20261028,
				Today:       clock.DateOf(time.Now()),
				PartnerPort: ports[0],
				WidgetPort:  ports[1],
				ControlPort: ports[2],
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := cfg.AddPartner(config.PartnerOptions{
				ID:           "balance",
				Name:         "Balance",
				LegalName:    "Evolution Labs",
				Purpose:      "to keep your books in sync with this account",
				RedirectURIs: []string{"https://balance.localhost/bank_connections/callback"},
				WebhookURL:   s.sink.srv.URL + "/webhooks",
			}); err != nil {
				t.Fatal(err)
			}
			app, err := connect.New(cfg, connect.Options{})
			if err != nil {
				t.Fatal(err)
			}
			if err := app.Clock().SetPace(0); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- app.Serve(ctx) }()
			select {
			case lastErr = <-done:
				cancel()
				continue
			case <-time.After(300 * time.Millisecond):
			}
			s.cfg, s.app = cfg, app
			t.Cleanup(func() {
				cancel()
				if err := <-done; err != nil {
					t.Errorf("serve: %v", err)
				}
			})
		}
		if s.app == nil {
			t.Fatalf("could not start the stack: %v", lastErr)
		}
	}
	s.redirect = s.cfg.Partners[0].RedirectURIs[0]
	key, err := config.ReadSecret(s.cfg.Path("partners", "balance", "api-key"))
	if err != nil {
		t.Fatal(err)
	}
	s.apiKey = key
	if s.ctlToken, err = config.ReadSecret(s.cfg.Path("control-token")); err != nil {
		t.Fatal(err)
	}
	spec, err := os.ReadFile(specPath())
	if err != nil {
		t.Fatal(err)
	}
	doc, err := libopenapi.NewDocument(spec)
	if err != nil {
		t.Fatal(err)
	}
	v, errs := validator.NewValidator(doc)
	if len(errs) > 0 {
		t.Fatalf("validator: %v", errs)
	}
	s.validator = v
	cert, err := tls.LoadX509KeyPair(s.cfg.Path("partners", "balance", "client.crt"), s.cfg.Path("partners", "balance", "client.key"))
	if err != nil {
		t.Fatal(err)
	}
	pool := s.rootPool()
	s.api = &http.Client{Timeout: 30 * time.Second, Transport: &validating{s: s, base: &http.Transport{TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: pool}}}}
	s.raw = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	s.waitHealthy()
	t.Cleanup(func() {
		s.vmu.Lock()
		defer s.vmu.Unlock()
		for _, v := range s.violations {
			t.Errorf("contract violation: %s", v)
		}
	})
	return s
}

func (s *Stack) rootPool() *x509.CertPool {
	pool := x509.NewCertPool()
	b, err := os.ReadFile(s.cfg.Path("partners", "balance", "ca.crt"))
	if err != nil {
		s.t.Fatal(err)
	}
	pool.AppendCertsFromPEM(b)
	return pool
}

func (s *Stack) waitHealthy() {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := s.ctlTry("GET", "/health", nil); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.t.Fatal("stack did not become healthy")
}

type validating struct {
	s    *Stack
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
	v.s.vmu.Lock()
	defer v.s.vmu.Unlock()
	if ok, errs := v.s.validator.ValidateHttpResponse(check, &vresp); !ok {
		for _, e := range errs {
			detail := []string{}
			for _, f := range e.SchemaValidationErrors {
				detail = append(detail, f.FieldPath+": "+f.Reason)
			}
			v.s.violations = append(v.s.violations, fmt.Sprintf("%s %s -> %d: %s %s %v", req.Method, req.URL.Path, resp.StatusCode, e.Message, e.Reason, detail))
		}
	}
	if resp.StatusCode < 300 {
		check2 := req.Clone(context.Background())
		check2.Body = io.NopCloser(bytes.NewReader(reqBody))
		if ok, errs := v.s.validator.ValidateHttpRequest(check2); !ok {
			for _, e := range errs {
				v.s.violations = append(v.s.violations, fmt.Sprintf("request %s %s: %s %s", req.Method, req.URL.Path, e.Message, e.Reason))
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

func noIdem() callOpt {
	return func(r *http.Request) { r.Header.Del("Idempotency-Key") }
}

func skipValidation() callOpt {
	return func(r *http.Request) { r.Header.Set("X-Test-Skip-Validation", "1") }
}

func apiKey(k string) callOpt {
	return func(r *http.Request) {
		if k == "" {
			r.Header.Del("X-Api-Key")
		} else {
			r.Header.Set("X-Api-Key", k)
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
	req.Header.Set("X-Api-Key", s.apiKey)
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

func (s *Stack) ctlTry(method, path string, body any) (map[string]any, error) {
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
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out := map[string]any{}
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &out)
	if resp.StatusCode >= 300 {
		return out, fmt.Errorf("control %s %s: HTTP %d %s", method, path, resp.StatusCode, b)
	}
	return out, nil
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

func (s *Stack) advance(by string) {
	s.t.Helper()
	s.ctl("POST", "/clock/advance", map[string]string{"by": by})
	s.syncNow()
}

func (s *Stack) today() clock.Date {
	out := s.ctl("GET", "/clock", nil)
	d, err := clock.ParseDate(fmt.Sprint(out["business_date"]))
	if err != nil {
		s.t.Fatal(err)
	}
	return d
}

type persona struct {
	ID, Username, Password string
}

func (s *Stack) persona(id string) persona {
	out := s.ctl("GET", "/personas", nil)
	for _, p := range out["data"].([]any) {
		m := p.(map[string]any)
		if m["id"] == id {
			return persona{ID: id, Username: fmt.Sprint(m["username"]), Password: fmt.Sprint(m["password"])}
		}
	}
	s.t.Fatalf("no persona %s", id)
	return persona{}
}

var (
	reCSRF    = regexp.MustCompile(`name="csrf" value="([^"]+)"`)
	reOTP     = regexp.MustCompile(`your code is <strong>(\d{6})</strong>`)
	reAccount = regexp.MustCompile(`name="account" value="([^"]+)"`)
)

type browser struct {
	s      *Stack
	c      *http.Client
	last   string
	result *url.URL
}

func (s *Stack) browser() *browser {
	jar, _ := cookiejar.New(nil)
	b := &browser{s: s}
	wu, _ := url.Parse(s.cfg.WidgetURL())
	widgetHost := wu.Host
	b.c = &http.Client{
		Jar:       jar,
		Timeout:   30 * time.Second,
		Transport: s.raw.Transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Host != widgetHost {
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
	return resp.StatusCode
}

func (b *browser) post(path string, form url.Values) int {
	b.s.t.Helper()
	if m := reCSRF.FindStringSubmatch(b.last); m != nil && form.Get("csrf") == "" {
		form.Set("csrf", m[1])
	}
	req, _ := http.NewRequest("POST", b.s.cfg.WidgetURL()+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", b.s.cfg.WidgetURL())
	resp, err := b.c.Do(req)
	if err != nil {
		b.s.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	b.last = string(body)
	return resp.StatusCode
}

type linkResult struct {
	Code, SessionID, State, Error string
}

func (s *Stack) completeLink(widgetURL, institution, personaID string, accounts func([]string) []string) linkResult {
	s.t.Helper()
	b := s.browser()
	b.get(widgetURL)
	if b.result == nil && institution != "" && strings.Contains(b.last, `name="institution"`) {
		b.post("/link/institution", url.Values{"institution": {institution}})
	}
	if b.result == nil {
		p := s.persona(personaID)
		b.post("/bank/login", url.Values{"username": {p.Username}, "password": {p.Password}})
		if b.result != nil {
			q := b.result.Query()
			return linkResult{SessionID: q.Get("link_session_id"), State: q.Get("state"), Error: q.Get("error")}
		}
		m := reOTP.FindStringSubmatch(b.last)
		if m == nil {
			s.t.Fatalf("no OTP on page: %.400s", b.last)
		}
		b.post("/bank/otp", url.Values{"otp": {m[1]}})
		all := []string{}
		for _, m := range reAccount.FindAllStringSubmatch(b.last, -1) {
			all = append(all, m[1])
		}
		if len(all) == 0 {
			s.t.Fatalf("no accounts on consent page: %.400s", b.last)
		}
		pick := all
		if accounts != nil {
			pick = accounts(all)
		}
		b.post("/bank/consent", url.Values{"account": pick, "action": {"approve"}})
	}
	if b.result == nil {
		s.t.Fatalf("widget did not redirect to the partner: %.400s", b.last)
	}
	q := b.result.Query()
	return linkResult{Code: q.Get("code"), SessionID: q.Get("link_session_id"), State: q.Get("state"), Error: q.Get("error")}
}

type connected struct {
	ConnectionID string
	Accounts     []map[string]any
	StartDate    clock.Date
}

func (s *Stack) connect(personaID, institution string, start clock.Date) connected {
	s.t.Helper()
	ls := s.must(s.call("POST", "/v1/link_sessions", map[string]any{"start_date": start.String(), "workspace_id": "ws_" + personaID, "redirect_uri": s.redirect, "state": "st-" + personaID}), 201)
	res := s.completeLink(fmt.Sprint(ls.JSON["widget_url"]), institution, personaID, nil)
	if res.Error != "" || res.Code == "" {
		s.t.Fatalf("link failed: %+v", res)
	}
	if res.SessionID != ls.JSON["id"] || res.State != "st-"+personaID {
		s.t.Fatalf("redirect carried the wrong session or state: %+v", res)
	}
	conn := s.must(s.call("POST", "/v1/connections/exchange", map[string]any{"code": res.Code, "link_session_id": res.SessionID}), 201)
	id := fmt.Sprint(conn.JSON["id"])
	s.syncNow()
	got := s.must(s.call("GET", "/v1/connections/"+id, nil), 200)
	if got.JSON["status"] != "active" {
		s.t.Fatalf("connection not active after sync: %s", got.Body)
	}
	accts := s.must(s.call("GET", "/v1/connections/"+id+"/accounts", nil), 200)
	out := connected{ConnectionID: id, StartDate: start}
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

func (s *Stack) syncAll(accountID, cursor string, limit int) (*feedState, Resp) {
	s.t.Helper()
	st := &feedState{lines: map[string]map[string]any{}, removed: map[string]bool{}, cursor: cursor}
	return s.syncMore(accountID, st, limit)
}

func (s *Stack) syncMore(accountID string, st *feedState, limit int) (*feedState, Resp) {
	s.t.Helper()
	for i := 0; i < 10000; i++ {
		q := url.Values{"limit": {strconv.Itoa(limit)}}
		if st.cursor != "" {
			q.Set("cursor", st.cursor)
		}
		r := s.call("GET", "/v1/accounts/"+accountID+"/transactions/sync?"+q.Encode(), nil)
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

func verifyV1a(pub ed25519.PublicKey, h hook) error {
	id := h.Header.Get("webhook-id")
	tsHeader := h.Header.Get("webhook-timestamp")
	sec, err := strconv.ParseInt(tsHeader, 10, 64)
	if err != nil {
		return fmt.Errorf("bad timestamp %q", tsHeader)
	}
	if d := time.Since(time.Unix(sec, 0)); d > 5*time.Minute || d < -5*time.Minute {
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
	out := s.ctl("GET", "/webhooks/deliveries?limit=100000", nil)
	evs := []map[string]any{}
	data := out["data"].([]any)
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
