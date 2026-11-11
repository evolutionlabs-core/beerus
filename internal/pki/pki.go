package pki

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

type Authority struct {
	Cert *x509.Certificate
	Key  crypto.Signer
	PEM  []byte
}

type Leaf struct {
	CertPEM []byte
	KeyPEM  []byte
}

var permittedIPs = []*net.IPNet{
	{IP: net.IPv4(127, 0, 0, 0).To4(), Mask: net.CIDRMask(8, 32)},
	{IP: net.IPv6loopback, Mask: net.CIDRMask(128, 128)},
}

func newKey() (*ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	return key, nil
}

func serial() (*big.Int, error) {
	sn, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}
	return sn, nil
}

func WriteFile(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", path, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return fmt.Errorf("set mode on %s: %w", tmp.Name(), err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return fmt.Errorf("write %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("close %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("rename to %s: %w", path, err)
	}
	return nil
}

func NewAuthority(commonName string, lifetime time.Duration) (*Authority, error) {
	key, err := newKey()
	if err != nil {
		return nil, err
	}
	sn, err := serial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:                sn,
		Subject:                     pkix.Name{CommonName: commonName, Organization: []string{"Beerus"}},
		NotBefore:                   now.Add(-time.Minute),
		NotAfter:                    now.Add(lifetime),
		IsCA:                        true,
		BasicConstraintsValid:       true,
		MaxPathLenZero:              true,
		KeyUsage:                    x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         []string{"localhost"},
		PermittedIPRanges:           permittedIPs,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create authority certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse authority certificate: %w", err)
	}
	return &Authority{Cert: cert, Key: key, PEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}, nil
}

func (a *Authority) issue(tmpl *x509.Certificate) (*Leaf, error) {
	key, err := newKey()
	if err != nil {
		return nil, err
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.Cert, &key.PublicKey, a.Key)
	if err != nil {
		return nil, fmt.Errorf("create leaf certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal leaf key: %w", err)
	}
	return &Leaf{
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	}, nil
}

func (a *Authority) IssueServer(names []string, lifetime time.Duration) (*Leaf, error) {
	sn, err := serial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: sn,
		Subject:      pkix.Name{CommonName: names[0], Organization: []string{"Beerus"}},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(lifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     names,
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	return a.issue(tmpl)
}

func (a *Authority) IssueClient(commonName, role string, lifetime time.Duration) (*Leaf, error) {
	sn, err := serial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: sn,
		Subject:      pkix.Name{CommonName: commonName, Organization: []string{"Beerus"}, OrganizationalUnit: []string{role}},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(lifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	return a.issue(tmpl)
}

func (a *Authority) IssueSigner(commonName, purpose string, lifetime time.Duration) (*Leaf, error) {
	sn, err := serial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: sn,
		Subject:      pkix.Name{CommonName: commonName, Organization: []string{"Beerus"}, OrganizationalUnit: []string{purpose}},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(lifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	return a.issue(tmpl)
}

func (a *Authority) Save(dir, name string) error {
	keyDER, err := x509.MarshalPKCS8PrivateKey(a.Key)
	if err != nil {
		return fmt.Errorf("marshal authority key: %w", err)
	}
	if err := WriteFile(filepath.Join(dir, name+".crt"), a.PEM, 0o644); err != nil {
		return err
	}
	return WriteFile(filepath.Join(dir, name+".key"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600)
}

func LoadAuthority(dir, name string) (*Authority, error) {
	certPEM, err := os.ReadFile(filepath.Join(dir, name+".crt"))
	if err != nil {
		return nil, fmt.Errorf("read authority certificate: %w", err)
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, name+".key"))
	if err != nil {
		return nil, fmt.Errorf("read authority key: %w", err)
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("load authority key pair %s: %w", name, err)
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse authority certificate %s: %w", name, err)
	}
	signer, ok := pair.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, errors.New("pki: key is not a signer")
	}
	return &Authority{Cert: cert, Key: signer, PEM: certPEM}, nil
}

func (l *Leaf) Save(dir, name string) error {
	if err := WriteFile(filepath.Join(dir, name+".crt"), l.CertPEM, 0o644); err != nil {
		return err
	}
	return WriteFile(filepath.Join(dir, name+".key"), l.KeyPEM, 0o600)
}

func LoadPool(paths ...string) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("read certificates: %w", err)
		}
		if !pool.AppendCertsFromPEM(b) {
			return nil, fmt.Errorf("pki: no certificates in %s", p)
		}
	}
	return pool, nil
}

func Thumbprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

func NeedsRenewal(certPath string, at time.Time) bool {
	b, err := os.ReadFile(certPath)
	if err != nil {
		return true
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return true
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return true
	}
	life := cert.NotAfter.Sub(cert.NotBefore)
	return at.After(cert.NotBefore.Add(life * 2 / 3))
}

func FileThumbprint(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read certificate: %w", err)
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return "", fmt.Errorf("pki: no certificate in %s", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parse certificate %s: %w", path, err)
	}
	return Thumbprint(cert), nil
}
