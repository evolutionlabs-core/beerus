package participant

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/nip"
)

func TestBankCallsAnswerWithTheRightCode(t *testing.T) {
	now := clock.New(nil).Now()
	stage := t.TempDir()
	cfg := &config.Config{Dir: stage, Connect: config.Connect{Host: "connect.localhost", BrandName: "Connect", NIBSSPort: 1, NIBSSControlPort: 2}}
	if err := config.Prepare(cfg, now); err != nil {
		t.Fatal(err)
	}
	if err := cfg.EnsureNIBSSDir("wld_test", now); err != nil {
		t.Fatal(err)
	}
	own := &config.Bank{ID: "bank-a", Name: "Bank A", Type: "commercial", Port: 3, APIPort: 4, Database: config.DatabaseName(stage, "bank-a")}
	if err := cfg.EnsureBankDir(own, now); err != nil {
		t.Fatal(err)
	}
	callers, err := newBankCaller(config.NIBSSDir(stage), false)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(stage, "banks", "bank-a")
	pair, err := tls.LoadX509KeyPair(filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key"))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	var current atomic.Pointer[http.HandlerFunc]
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { (*current.Load())(w, r) })}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	url := "https://bank-a.localhost:" + strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	member := func(identity string) nibss.Member {
		return nibss.Member{Institution: nip.Institution{ID: "bank-a", Name: "Bank A"}, CreditURL: url, CertificateIdentity: identity}
	}
	reply := func(status int, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}
	}
	rows := []struct {
		name     string
		handler  http.HandlerFunc
		identity string
		code     string
		account  string
	}{
		{"a name", reply(200, `{"response_code":"00","account_name":"ADA OBI"}`), "bank-a", "00", "ADA OBI"},
		{"invalid account", reply(200, `{"response_code":"07"}`), "bank-a", "07", ""},
		{"no record", reply(200, `{"response_code":"25"}`), "bank-a", "25", ""},
		{"bank unavailable", reply(200, `{"response_code":"91"}`), "bank-a", "91", ""},
		{"server error", reply(500, `{"error":{"code":"internal_error"}}`), "bank-a", "91", ""},
		{"closed bank", reply(409, `{"error":{"code":"bank_closed"}}`), "bank-a", "91", ""},
		{"refused certificate", reply(403, `{"error":{"code":"forbidden"}}`), "bank-a", "91", ""},
		{"garbage", reply(200, `<html>`), "bank-a", "91", ""},
		{"empty body", reply(200, ``), "bank-a", "91", ""},
		{"unknown response code", reply(200, `{"response_code":"96"}`), "bank-a", "91", ""},
		{"oversized body", reply(200, `{"response_code":"00","account_name":"`+strings.Repeat("A", maxBankBody)+`"}`), "bank-a", "91", ""},
		{"redirect", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/elsewhere", http.StatusFound) }, "bank-a", "91", ""},
		{"slower than the budget", func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }, "bank-a", "91", ""},
		{"another certificate identity", reply(200, `{"response_code":"00","account_name":"ADA OBI"}`), "bank-z", "91", ""},
	}
	for _, r := range rows {
		current.Store(&r.handler)
		m := member(r.identity)
		name, err := callers.nameAt(context.Background(), m, "0000000014")
		code := nip.OK
		if err != nil {
			code, _ = refusal(m, err)
		}
		if code != r.code || name != r.account {
			t.Errorf("%s: got %s %q (%v), want %s %q", r.name, code, name, err, r.code, r.account)
		}
	}
}
