package pki

import (
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestIssuedLeavesChainToAuthority(t *testing.T) {
	ca, err := NewAuthority("Test CA", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ca.Cert.IsCA || ca.Cert.Subject.CommonName != "Test CA" {
		t.Fatalf("authority = CA %v, CN %q", ca.Cert.IsCA, ca.Cert.Subject.CommonName)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)

	server, err := ca.IssueServer([]string{"bank.localhost", "localhost"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	client, err := ca.IssueClient("partner-1", "partner", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ca.IssueSigner("bank-events", "event-signer", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		leaf   *Leaf
		cn     string
		ou     string
		dns    []string
		ips    []net.IP
		usages []x509.ExtKeyUsage
		verify x509.ExtKeyUsage
	}{
		{"server", server, "bank.localhost", "", []string{"bank.localhost", "localhost"}, []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, x509.ExtKeyUsageServerAuth},
		{"client", client, "partner-1", "partner", nil, nil, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, x509.ExtKeyUsageClientAuth},
		{"signer", signer, "bank-events", "event-signer", nil, nil, nil, x509.ExtKeyUsageAny},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := tc.leaf.Save(dir, "leaf"); err != nil {
				t.Fatal(err)
			}
			fp, err := FileThumbprint(filepath.Join(dir, "leaf.crt"))
			if err != nil {
				t.Fatal(err)
			}
			certs, err := LoadPool(filepath.Join(dir, "leaf.crt"))
			if err != nil || certs == nil {
				t.Fatalf("load pool: %v", err)
			}
			cert := parseLeaf(t, tc.leaf)
			if fp != Thumbprint(cert) || len(fp) != 64 {
				t.Fatalf("thumbprint = %q", fp)
			}
			if cert.Subject.CommonName != tc.cn {
				t.Fatalf("CN = %q, want %q", cert.Subject.CommonName, tc.cn)
			}
			var ou string
			if len(cert.Subject.OrganizationalUnit) == 1 {
				ou = cert.Subject.OrganizationalUnit[0]
			}
			if ou != tc.ou {
				t.Fatalf("OU = %q, want %q", ou, tc.ou)
			}
			if !reflect.DeepEqual(cert.DNSNames, tc.dns) {
				t.Fatalf("DNS names = %v, want %v", cert.DNSNames, tc.dns)
			}
			if len(cert.IPAddresses) != len(tc.ips) {
				t.Fatalf("IPs = %v, want %v", cert.IPAddresses, tc.ips)
			}
			for i, ip := range tc.ips {
				if !cert.IPAddresses[i].Equal(ip) {
					t.Fatalf("IP %d = %v, want %v", i, cert.IPAddresses[i], ip)
				}
			}
			if !reflect.DeepEqual(cert.ExtKeyUsage, tc.usages) {
				t.Fatalf("ext key usage = %v, want %v", cert.ExtKeyUsage, tc.usages)
			}
			if cert.KeyUsage != x509.KeyUsageDigitalSignature || cert.IsCA {
				t.Fatalf("key usage = %v, CA = %v", cert.KeyUsage, cert.IsCA)
			}
			opts := x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{tc.verify}}
			if _, err := cert.Verify(opts); err != nil {
				t.Fatalf("verify: %v", err)
			}
			if tc.verify == x509.ExtKeyUsageServerAuth {
				opts.DNSName = "bank.localhost"
				if _, err := cert.Verify(opts); err != nil {
					t.Fatalf("verify name: %v", err)
				}
				opts.DNSName = "other.localhost"
				if _, err := cert.Verify(opts); err == nil {
					t.Fatal("verified a name outside the SANs")
				}
			}
		})
	}
}

func TestAuthoritySaveLoadRoundTrip(t *testing.T) {
	ca, err := NewAuthority("Round Trip CA", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := ca.Save(dir, "root"); err != nil {
		t.Fatal(err)
	}
	for file, mode := range map[string]os.FileMode{"root.crt": 0o644, "root.key": 0o600} {
		info, err := os.Stat(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Fatalf("%s mode = %o, want %o", file, info.Mode().Perm(), mode)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("dir has %d files, want 2", len(entries))
	}
	loaded, err := LoadAuthority(dir, "root")
	if err != nil {
		t.Fatal(err)
	}
	if Thumbprint(loaded.Cert) != Thumbprint(ca.Cert) || string(loaded.PEM) != string(ca.PEM) {
		t.Fatal("loaded authority differs from saved one")
	}
	leaf, err := loaded.IssueServer([]string{"localhost"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	if _, err := parseLeaf(t, leaf).Verify(x509.VerifyOptions{Roots: pool}); err != nil {
		t.Fatalf("leaf from loaded authority: %v", err)
	}
}

func TestWriteFileReplacesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if err := WriteFile(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "two" {
		t.Fatalf("content = %q, err %v", b, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("dir entries = %d, err %v", len(entries), err)
	}
	err = WriteFile(filepath.Join(dir, "missing", "secret"), []byte("x"), 0o600)
	if err == nil || !strings.HasPrefix(err.Error(), "create temp file for ") {
		t.Fatalf("error = %v", err)
	}
}

func TestNeedsRenewal(t *testing.T) {
	ca, err := NewAuthority("Renewal CA", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := ca.IssueServer([]string{"localhost"}, 90*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := leaf.Save(dir, "server"); err != nil {
		t.Fatal(err)
	}
	cert := parseLeaf(t, leaf)
	threshold := cert.NotBefore.Add(cert.NotAfter.Sub(cert.NotBefore) * 2 / 3)
	path := filepath.Join(dir, "server.crt")
	if err := os.WriteFile(filepath.Join(dir, "bad.crt"), []byte("not pem"), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		path string
		at   time.Time
		want bool
	}{
		{"just before threshold", path, threshold, false},
		{"just after threshold", path, threshold.Add(time.Nanosecond), true},
		{"at issue", path, cert.NotBefore, false},
		{"after expiry", path, cert.NotAfter.Add(time.Hour), true},
		{"missing file", filepath.Join(dir, "none.crt"), threshold.Add(-time.Hour), true},
		{"not a certificate", filepath.Join(dir, "bad.crt"), cert.NotBefore, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NeedsRenewal(tc.path, tc.at); got != tc.want {
				t.Fatalf("NeedsRenewal = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLoadFailureMessages(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadAuthority(dir, "root")
	if err == nil || !strings.HasPrefix(err.Error(), "read authority certificate: ") {
		t.Fatalf("LoadAuthority error = %v", err)
	}
	empty := filepath.Join(dir, "empty.pem")
	if err := os.WriteFile(empty, []byte("nothing"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPool(empty); err == nil || err.Error() != "pki: no certificates in "+empty {
		t.Fatalf("LoadPool error = %v", err)
	}
	if _, err := FileThumbprint(empty); err == nil || err.Error() != "pki: no certificate in "+empty {
		t.Fatalf("FileThumbprint error = %v", err)
	}
}

func parseLeaf(t *testing.T, l *Leaf) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(l.CertPEM)
	if block == nil {
		t.Fatal("leaf has no certificate block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}
