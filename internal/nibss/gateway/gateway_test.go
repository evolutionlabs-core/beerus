package gateway_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pb33f/libopenapi"
	validator "github.com/pb33f/libopenapi-validator"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/nibss/gateway"
	"github.com/evolutionlabs-core/beerus/internal/pki"
)

const (
	specFile    = "../../../api/nip/openapi.yaml"
	certLife    = time.Hour
	clientLimit = 10 * time.Second
	forbidden   = `{"error":{"code":"forbidden","message":"This certificate may not read the bank directory. Only banks may.","request_id":"req_ID"}}`
	unavailable = `{"error":{"code":"unavailable","message":"NIBSS is not available right now. Try again soon.","request_id":"req_ID"}}`
)

var requestID = regexp.MustCompile(`"request_id":"req_[A-Za-z0-9]+"`)

var directory = []nibss.Institution{
	{ID: "bank-b", Name: "Bank B", NIPCode: "000015", NUBANCode: "015", Type: "digital", Status: nibss.StatusSuspended},
	{ID: "bank-a", Name: "Bank A", NIPCode: "000014", NUBANCode: "014", Type: "commercial", Status: nibss.StatusActive},
	{ID: "bank-c", Name: "Bank C", NIPCode: "000016", NUBANCode: "016", Type: "microfinance", Status: nibss.StatusTerminating},
	{ID: "bank-d", Name: "Bank D", NIPCode: "000017", NUBANCode: "017", Type: "commercial", Status: nibss.StatusRemoved},
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

type stack struct {
	base    string
	clients map[string]*http.Client
	spec    validator.Validator
	down    atomic.Bool
}

func newStack(t *testing.T) *stack {
	t.Helper()
	s := &stack{clients: map[string]*http.Client{}}
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
	list := func(context.Context) ([]nibss.Institution, error) {
		if s.down.Load() {
			return nil, errors.New("directory closed")
		}
		return directory, nil
	}
	srv := &http.Server{
		Handler:           gateway.New(list, slog.New(slog.NewTextHandler(io.Discard, nil))),
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
	for name, id := range map[string]string{"bank-a": "bank-a", "bank-d": "bank-d", "stranger": "bank-z", "aggregator": "beerus-connect"} {
		role := config.BankRole
		if name == "aggregator" {
			role = "aggregator"
		}
		leaf, err := ca.IssueClient(id, role, certLife)
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
	status int
	body   string
	allow  string
}

func (s *stack) call(t *testing.T, client, method, path string, validate bool) reply {
	t.Helper()
	newRequest := func() *http.Request {
		req, err := http.NewRequest(method, s.base+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	if validate {
		if ok, errs := s.spec.ValidateHttpRequest(newRequest()); !ok {
			t.Fatalf("%s %s request: %v", method, path, errs)
		}
	}
	resp, err := s.clients[client].Do(newRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if validate {
		vresp := &http.Response{StatusCode: resp.StatusCode, Header: resp.Header, Body: io.NopCloser(bytes.NewReader(raw))}
		if ok, errs := s.spec.ValidateHttpResponse(newRequest(), vresp); !ok {
			t.Fatalf("%s %s response %d %s: %v", method, path, resp.StatusCode, raw, errs)
		}
	}
	return reply{resp.StatusCode, requestID.ReplaceAllString(strings.TrimSpace(string(raw)), `"request_id":"req_ID"`), resp.Header.Get("Allow")}
}

func bankJSON(in nibss.Institution) string {
	return fmt.Sprintf(`{"id":%q,"name":%q,"nip_code":%q,"nuban_code":%q,"type":%q,"status":%q}`, in.ID, in.Name, in.NIPCode, in.NUBANCode, in.Type, in.Status)
}

func TestBankDirectoryContract(t *testing.T) {
	s := newStack(t)
	a, b, c := bankJSON(directory[1]), bankJSON(directory[0]), bankJSON(directory[2])
	rows := []struct {
		name         string
		client       string
		method, path string
		validate     bool
		down         bool
		status       int
		want         string
		allow        string
	}{
		{name: "whole directory", method: "GET", path: "/v1/banks", validate: true, status: 200, want: fmt.Sprintf(`{"data":[%s,%s,%s]}`, a, b, c)},
		{name: "first page", method: "GET", path: "/v1/banks?limit=2", validate: true, status: 200, want: fmt.Sprintf(`{"data":[%s,%s],"next_cursor":"000015"}`, a, b)},
		{name: "last page", method: "GET", path: "/v1/banks?limit=2&cursor=000015", validate: true, status: 200, want: fmt.Sprintf(`{"data":[%s]}`, c)},
		{name: "after the last bank", method: "GET", path: "/v1/banks?cursor=000016", validate: true, status: 200, want: `{"data":[]}`},
		{name: "limit too large", method: "GET", path: "/v1/banks?limit=501", status: 400, want: `{"error":{"code":"invalid_request","message":"limit must be a number from 1 to 500.","request_id":"req_ID"}}`},
		{name: "limit not a number", method: "GET", path: "/v1/banks?limit=many", status: 400, want: `{"error":{"code":"invalid_request","message":"limit must be a number from 1 to 500.","request_id":"req_ID"}}`},
		{name: "cursor not in the directory", method: "GET", path: "/v1/banks?cursor=999999", validate: true, status: 400, want: `{"error":{"code":"invalid_cursor","message":"That cursor is not valid. Use the next_cursor value from the last page.","request_id":"req_ID"}}`},
		{name: "cursor of a removed bank", method: "GET", path: "/v1/banks?cursor=000017", status: 400, want: `{"error":{"code":"invalid_cursor","message":"That cursor is not valid. Use the next_cursor value from the last page.","request_id":"req_ID"}}`},
		{name: "aggregator certificate", client: "aggregator", method: "GET", path: "/v1/banks", validate: true, status: 403, want: forbidden},
		{name: "bank NIBSS does not know", client: "stranger", method: "GET", path: "/v1/banks", validate: true, status: 403, want: `{"error":{"code":"forbidden","message":"This certificate may not read the bank directory. bank-z is not a bank NIBSS knows.","request_id":"req_ID"}}`},
		{name: "removed bank", client: "bank-d", method: "GET", path: "/v1/banks", status: 403, want: `{"error":{"code":"forbidden","message":"This certificate may not read the bank directory. bank-d is not a bank NIBSS knows.","request_id":"req_ID"}}`},
		{name: "wrong method", method: "POST", path: "/v1/banks", status: 405, allow: "GET", want: `{"error":{"code":"method_not_allowed","message":"Use GET for this path.","request_id":"req_ID"}}`},
		{name: "unknown path", method: "GET", path: "/v1/nothing", status: 404, want: `{"error":{"code":"not_found","message":"This path is not part of the NIBSS API.","request_id":"req_ID"}}`},
		{name: "directory closed", method: "GET", path: "/v1/banks", validate: true, down: true, status: 503, want: unavailable},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			s.down.Store(r.down)
			client := r.client
			if client == "" {
				client = "bank-a"
			}
			got := s.call(t, client, r.method, r.path, r.validate)
			if got.status != r.status || got.body != r.want || got.allow != r.allow {
				t.Fatalf("got %d %s allow=%q, want %d %s allow=%q", got.status, got.body, got.allow, r.status, r.want, r.allow)
			}
		})
	}
}
