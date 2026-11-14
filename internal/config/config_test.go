package config

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"strings"
	"testing"
	"time"
)

func finish(c Config) (*Config, error) {
	c.Dir, c.TSQAfter, c.TSQAttempts = "x", 2*time.Minute, 3
	return &c, c.Finish()
}

func TestStatusQueriesNeedAWaitAndTries(t *testing.T) {
	for _, c := range []Config{{Dir: "x", TSQAttempts: 3}, {Dir: "x", TSQAfter: time.Minute}} {
		err := c.Finish()
		if err == nil || !strings.Contains(err.Error(), "nip status queries need a wait and a number of tries") {
			t.Errorf("%s and %d: %v", c.TSQAfter, c.TSQAttempts, err)
		}
	}
}

func TestNIPSessionsAreValidated(t *testing.T) {
	c, err := finish(Config{})
	if err != nil || c.Sessions() != DefaultNIPSessions {
		t.Fatalf("default: %v %v", c, err)
	}
	for _, ok := range []int{1, 2, 12, 24, 1440} {
		if _, err := finish(Config{NIPSessions: ok}); err != nil {
			t.Errorf("nip sessions %d: %v", ok, err)
		}
	}
	for _, bad := range []int{-1, 7, 11, 2880, 1000000000} {
		_, err := finish(Config{NIPSessions: bad})
		if err == nil || !strings.Contains(err.Error(), "nip sessions") {
			t.Errorf("nip sessions %d: %v", bad, err)
		}
	}
}

func TestPortAllocation(t *testing.T) {
	c, err := finish(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if c.APIPortOffset() != DefaultAPIPortOffset || c.PortWindow() != DefaultPortWindow {
		t.Fatalf("defaults %d %d", c.APIPortOffset(), c.PortWindow())
	}
	used := map[int]bool{8010: true, 8011: true}
	if p, err := c.AllocatePort(8010, used, func(p int) bool { return p != 8012 }); err != nil || p != 8013 {
		t.Fatalf("port %d: %v", p, err)
	}
	if _, err := c.AllocatePort(8010, used, func(int) bool { return false }); err == nil || !strings.Contains(err.Error(), "8010 to 8209") {
		t.Fatalf("no port free: %v", err)
	}
	c, err = finish(Config{Ports: Ports{APIOffset: 20, Window: 3}})
	if err != nil || c.APIPortOffset() != 20 {
		t.Fatalf("custom: %v", err)
	}
	if _, err := c.AllocatePort(9000, map[int]bool{9000: true, 9001: true, 9002: true}, func(int) bool { return true }); err == nil {
		t.Fatal("the window should stop the search")
	}
	if _, err := finish(Config{Ports: Ports{Window: -1}}); err == nil {
		t.Fatal("negative window accepted")
	}
	if _, err := finish(Config{Banks: []*Bank{{ID: "bank-a", API: BankAPI{DormantDays: -5}}}}); err == nil || !strings.Contains(err.Error(), "dormant_days") {
		t.Fatalf("negative dormant_days: %v", err)
	}
}

func TestNIBSSClientCertificate(t *testing.T) {
	c, err := finish(Config{})
	if err != nil {
		t.Fatal(err)
	}
	c.Dir = t.TempDir()
	now := time.Now()
	if err := Prepare(c, now); err != nil {
		t.Fatal(err)
	}
	crt, key := c.Path("nibss", "client.crt"), c.Path("nibss", "client.key")
	pair, err := tls.LoadX509KeyPair(crt, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if leaf.Subject.CommonName != "beerus-nibss" || len(leaf.Subject.OrganizationalUnit) != 1 || leaf.Subject.OrganizationalUnit[0] != "nibss" {
		t.Fatalf("subject %v, want CN beerus-nibss and role nibss", leaf.Subject)
	}
	if len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		t.Fatalf("usage %v, want client auth only", leaf.ExtKeyUsage)
	}
	info, err := os.Stat(key)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %v, want 0600", info.Mode().Perm())
	}
	if err := Prepare(c, now); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(crt)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pair.Certificate[0]})) {
		t.Fatal("a second prepare replaced the certificate")
	}
}

func TestBankClientAndNIBSSServerCertificates(t *testing.T) {
	c, err := finish(Config{Banks: []*Bank{{ID: "bank-a"}, {ID: "bank-b"}}})
	if err != nil {
		t.Fatal(err)
	}
	c.Dir = t.TempDir()
	if err := Prepare(c, time.Now()); err != nil {
		t.Fatal(err)
	}
	read := func(dir, name string) *x509.Certificate {
		t.Helper()
		pair, err := tls.LoadX509KeyPair(c.Path(dir, name+".crt"), c.Path(dir, name+".key"))
		if err != nil {
			t.Fatal(err)
		}
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		return leaf
	}
	for _, id := range []string{"bank-a", "bank-b"} {
		leaf := read("banks/"+id, "client")
		if leaf.Subject.CommonName != id || len(leaf.Subject.OrganizationalUnit) != 1 || leaf.Subject.OrganizationalUnit[0] != "bank" {
			t.Fatalf("%s subject %v, want CN %s and role bank", id, leaf.Subject, id)
		}
		if len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
			t.Fatalf("%s usage %v, want client auth only", id, leaf.ExtKeyUsage)
		}
	}
	server := read("nibss", "server")
	if len(server.DNSNames) != 2 || server.DNSNames[0] != "nibss.localhost" || server.DNSNames[1] != "localhost" {
		t.Fatalf("NIBSS server names %v, want nibss.localhost and localhost", server.DNSNames)
	}
}
