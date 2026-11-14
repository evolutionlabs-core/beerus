package inbound_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pb33f/libopenapi"
	validator "github.com/pb33f/libopenapi-validator"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/banktest"
	"github.com/evolutionlabs-core/beerus/internal/bank/inbound"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/pki"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

const (
	specFile    = "../../../api/nip/openapi.yaml"
	nipCode     = "999001"
	nubanCode   = "901"
	nibssCN     = "beerus-nibss"
	nibssRole   = "nibss"
	startAmount = 50000000
	certLife    = time.Hour
	clientLimit = 10 * time.Second
	bodyLimit   = 65536
)

var (
	t0        = time.Date(2026, 11, 14, 10, 30, 0, 0, clock.Lagos)
	requestID = regexp.MustCompile(`"request_id":"req_[A-Za-z0-9]+"`)
)

type stack struct {
	bank    *bank.Bank
	base    string
	clients map[string]*http.Client
	spec    validator.Validator
	funded  string
	empty   string
}

func session(n int) string {
	return fmt.Sprintf("%s261114103000%012d", nipCode, n)
}

func leafConfig(t *testing.T, leaf *pki.Leaf, ca *pki.Authority) *tls.Config {
	t.Helper()
	pair, err := tls.X509KeyPair(leaf.CertPEM, leaf.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.PEM)
	return &tls.Config{Certificates: []tls.Certificate{pair}, RootCAs: pool, MinVersion: tls.VersionTLS12}
}

func newStack(t *testing.T) *stack {
	t.Helper()
	ctx := context.Background()
	b := bank.New(ledger.New(banktest.Schema(t), nil), bank.Profile{ID: "bank-a", Name: "Bank A", NIPCode: nipCode, NUBANCode: nubanCode}, banktest.Fees(t))
	if err := b.Setup(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.Genesis(ctx, 1000000000000, 200000000000, t0); err != nil {
		t.Fatal(err)
	}
	s := &stack{bank: b, clients: map[string]*http.Client{}}
	for i, deposit := range []int64{startAmount, 0} {
		c, err := b.CreateCustomer(ctx, fmt.Sprintf("c%d", i), "ADA OBI", "+2348030000001", clock.Date{Y: 1990, M: 1, D: 1}, "22912345678", "", "", t0)
		if err != nil {
			t.Fatal(err)
		}
		acc, err := b.OpenAccount(ctx, fmt.Sprintf("a%d", i), c.ID, bank.ProductCurrent, t0)
		if err != nil {
			t.Fatal(err)
		}
		if deposit > 0 {
			if _, err := b.CashDeposit(ctx, "open:"+acc.NUBAN, acc.NUBAN, deposit, t0); err != nil {
				t.Fatal(err)
			}
		}
		if i == 0 {
			s.funded = acc.NUBAN
		} else {
			s.empty = acc.NUBAN
		}
	}
	ca, err := pki.NewAuthority("Test CA", certLife)
	if err != nil {
		t.Fatal(err)
	}
	server, err := ca.IssueServer([]string{"localhost"}, certLife)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(server.CertPEM, server.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca.PEM)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		Handler:           inbound.New(b, func() time.Time { return t0 }, inbound.Caller{CommonName: nibssCN, Role: nibssRole}, slog.New(slog.NewTextHandler(io.Discard, nil))),
		ReadHeaderTimeout: clientLimit,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{pair}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool, MinVersion: tls.VersionTLS12},
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(tls.NewListener(ln, srv.TLSConfig))
	}()
	t.Cleanup(func() {
		_ = srv.Close()
		<-done
	})
	s.base = "https://" + ln.Addr().String()
	for name, role := range map[string]string{"nibss": nibssRole, "aggregator": "aggregator"} {
		cn := nibssCN
		if name == "aggregator" {
			cn = "beerus-connect"
		}
		leaf, err := ca.IssueClient(cn, role, certLife)
		if err != nil {
			t.Fatal(err)
		}
		tr := &http.Transport{TLSClientConfig: leafConfig(t, leaf, ca), IdleConnTimeout: clientLimit}
		t.Cleanup(tr.CloseIdleConnections)
		s.clients[name] = &http.Client{Transport: tr, Timeout: clientLimit}
	}
	spec, err := os.ReadFile(specFile)
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
	s.spec = v
	return s
}

type reply struct {
	status   int
	body     string
	replayed bool
}

func (s *stack) call(t *testing.T, client, method, path, body string, checkRequest bool) reply {
	t.Helper()
	if checkRequest {
		vreq, err := http.NewRequest(method, s.base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		vreq.Header.Set("Content-Type", "application/json")
		if ok, errs := s.spec.ValidateHttpRequest(vreq); !ok {
			t.Fatalf("%s %s request: %v", method, path, errs)
		}
	}
	req, err := http.NewRequest(method, s.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.clients[client].Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	vresp := &http.Response{StatusCode: resp.StatusCode, Header: resp.Header, Body: io.NopCloser(bytes.NewReader(raw))}
	vreq, err := http.NewRequest(method, s.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	vreq.Header.Set("Content-Type", "application/json")
	if ok, errs := s.spec.ValidateHttpResponse(vreq, vresp); !ok {
		t.Fatalf("%s %s response %d %s: %v", method, path, resp.StatusCode, raw, errs)
	}
	return reply{resp.StatusCode, requestID.ReplaceAllString(strings.TrimSpace(string(raw)), `"request_id":"req_ID"`), resp.Header.Get("Idempotent-Replayed") == "true"}
}

func (s *stack) balance(t *testing.T, nuban string) int64 {
	t.Helper()
	v, err := s.bank.L.Balance(context.Background(), bank.CustomerAccount(nuban))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func creditBody(sid, to string, amount int64, narration string) string {
	return fmt.Sprintf(`{"session_id":%q,"from_code":"000014","from_bank_name":"Bank B","from_nuban":"9010000014","from_name":"CHIDI OKAFOR","to_nuban":%q,"amount_minor":%d,"narration":%q,"created_at":"2026-11-14T10:30:00+01:00"}`, sid, to, amount, narration)
}

func badCheckDigit(nuban string) string {
	last := nuban[9]
	next := '0' + (last-'0'+1)%10
	return nuban[:9] + string(next)
}

func TestNIPContract(t *testing.T) {
	s := newStack(t)
	unknown := world.NUBAN(nubanCode, 999999)
	invalid := badCheckDigit(s.funded)
	rows := []struct {
		name         string
		client       string
		method, path string
		body         string
		skipRequest  bool
		status       int
		want         string
		replayed     bool
		funded       int64
		empty        int64
	}{
		{name: "name found", method: "GET", path: "/v1/nip/account-names/" + s.funded, status: 200, want: fmt.Sprintf(`{"response_code":"00","account_number":%q,"account_name":"ADA OBI"}`, s.funded), funded: startAmount},
		{name: "name invalid account", method: "GET", path: "/v1/nip/account-names/" + invalid, status: 200, want: fmt.Sprintf(`{"response_code":"07","account_number":%q}`, invalid), funded: startAmount},
		{name: "name unknown account", method: "GET", path: "/v1/nip/account-names/" + unknown, status: 200, want: fmt.Sprintf(`{"response_code":"25","account_number":%q}`, unknown), funded: startAmount},
		{name: "credit", method: "POST", path: "/v1/nip/credits", body: creditBody(session(1), s.funded, 2500000, "Rent for November"), status: 200, want: fmt.Sprintf(`{"session_id":%q,"response_code":"00"}`, session(1)), funded: startAmount + 2500000},
		{name: "credit repeat", method: "POST", path: "/v1/nip/credits", body: creditBody(session(1), s.funded, 2500000, "Rent for November"), status: 200, want: fmt.Sprintf(`{"session_id":%q,"response_code":"00"}`, session(1)), replayed: true, funded: startAmount + 2500000},
		{name: "credit reused session", method: "POST", path: "/v1/nip/credits", body: creditBody(session(1), s.funded, 3000000, "Rent for November"), status: 422, want: fmt.Sprintf(`{"error":{"code":"idempotency_key_reused","message":"Session %s was first credited with amount_minor 2500000, and this request has 3000000. Repeat the first request.","request_id":"req_ID"}}`, session(1)), funded: startAmount + 2500000},
		{name: "status credited", method: "GET", path: "/v1/nip/credits/" + session(1), status: 200, want: fmt.Sprintf(`{"session_id":%q,"response_code":"00"}`, session(1)), funded: startAmount + 2500000},
		{name: "status unknown", method: "GET", path: "/v1/nip/credits/" + session(2), status: 200, want: fmt.Sprintf(`{"session_id":%q,"response_code":"25"}`, session(2)), funded: startAmount + 2500000},
		{name: "credit after 25", method: "POST", path: "/v1/nip/credits", body: creditBody(session(2), s.funded, 1000000, ""), status: 200, want: fmt.Sprintf(`{"session_id":%q,"response_code":"25"}`, session(2)), funded: startAmount + 2500000},
		{name: "status after refused credit", method: "GET", path: "/v1/nip/credits/" + session(2), status: 200, want: fmt.Sprintf(`{"session_id":%q,"response_code":"25"}`, session(2)), funded: startAmount + 2500000},
		{name: "credit to invalid account", method: "POST", path: "/v1/nip/credits", body: creditBody(session(3), invalid, 1000000, ""), status: 200, want: fmt.Sprintf(`{"session_id":%q,"response_code":"07"}`, session(3)), funded: startAmount + 2500000},
		{name: "status of refused credit", method: "GET", path: "/v1/nip/credits/" + session(3), status: 200, want: fmt.Sprintf(`{"session_id":%q,"response_code":"25"}`, session(3)), funded: startAmount + 2500000},
		{name: "largest amount", method: "POST", path: "/v1/nip/credits", body: creditBody(session(4), s.empty, math.MaxInt64, ""), status: 200, want: fmt.Sprintf(`{"session_id":%q,"response_code":"00"}`, session(4)), funded: startAmount + 2500000, empty: math.MaxInt64},
		{name: "overflow", method: "POST", path: "/v1/nip/credits", body: creditBody(session(5), s.empty, 1, ""), status: 200, want: fmt.Sprintf(`{"session_id":%q,"response_code":"96"}`, session(5)), funded: startAmount + 2500000, empty: math.MaxInt64},
		{name: "wrong certificate", client: "aggregator", method: "GET", path: "/v1/nip/account-names/" + s.funded, status: 403, want: `{"error":{"code":"forbidden","message":"This certificate may not call the NIP API of this bank. Only NIBSS may.","request_id":"req_ID"}}`, funded: startAmount + 2500000, empty: math.MaxInt64},
		{name: "oversized body", method: "POST", path: "/v1/nip/credits", body: creditBody(session(6), s.funded, 1000000, strings.Repeat("x", bodyLimit)), skipRequest: true, status: 400, want: fmt.Sprintf(`{"error":{"code":"invalid_request","message":"The body is larger than %d bytes. Send a smaller body.","request_id":"req_ID"}}`, bodyLimit), funded: startAmount + 2500000, empty: math.MaxInt64},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			client := r.client
			if client == "" {
				client = "nibss"
			}
			got := s.call(t, client, r.method, r.path, r.body, !r.skipRequest)
			if got.status != r.status || got.body != r.want || got.replayed != r.replayed {
				t.Fatalf("got %d %s replayed=%v, want %d %s replayed=%v", got.status, got.body, got.replayed, r.status, r.want, r.replayed)
			}
			if f, e := s.balance(t, s.funded), s.balance(t, s.empty); f != r.funded || e != r.empty {
				t.Fatalf("balances %d and %d, want %d and %d", f, e, r.funded, r.empty)
			}
		})
	}
	check, err := s.bank.L.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !check.OK() {
		t.Fatal(check)
	}
}

func TestCreditRacesStatus(t *testing.T) {
	s := newStack(t)
	const sessions, parallel = 40, 8
	type outcome struct{ credit, status string }
	outcomes := make([]outcome, sessions)
	gate := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i := 0; i < sessions; i++ {
		sid := session(100 + i)
		for _, kind := range []string{"credit", "status"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				gate <- struct{}{}
				defer func() { <-gate }()
				var r reply
				if kind == "credit" {
					r = s.call(t, "nibss", "POST", "/v1/nip/credits", creditBody(sid, s.funded, 1000000, ""), true)
				} else {
					r = s.call(t, "nibss", "GET", "/v1/nip/credits/"+sid, "", true)
				}
				code := strings.Split(r.body, `"response_code":"`)[1][:2]
				if kind == "credit" {
					outcomes[i].credit = code
				} else {
					outcomes[i].status = code
				}
			}()
		}
	}
	wg.Wait()
	credited := int64(0)
	for i, o := range outcomes {
		switch {
		case o.credit == "00" && o.status == "00":
			credited++
		case o.credit == "25" && o.status == "25":
		default:
			t.Errorf("session %d: credit %s and status %s, want 00 and 00 or 25 and 25", i, o.credit, o.status)
		}
	}
	if got, want := s.balance(t, s.funded), startAmount+credited*1000000; got != want {
		t.Fatalf("balance %d, want %d after %d credits", got, want, credited)
	}
	var posted int64
	if err := s.bank.L.Pool().QueryRow(context.Background(), `SELECT count(*) FROM journal_entries WHERE kind = 'nip_inward'`).Scan(&posted); err != nil {
		t.Fatal(err)
	}
	if posted != credited {
		t.Fatalf("%d postings, want %d", posted, credited)
	}
	check, err := s.bank.L.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !check.OK() {
		t.Fatal(check)
	}
}
