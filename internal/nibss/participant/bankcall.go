package participant

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/pki"
)

const (
	bankCallTimeout = 2 * time.Second
	bankDialTimeout = time.Second
	bankIdleTimeout = 90 * time.Second
	bankIdleConns   = 2
	maxBankBody     = 64 << 10
	bankNamesPath   = "/v1/nip/account-names/"
	localhostSuffix = ".localhost"
	loopbackHost    = "127.0.0.1"
)

type bankCaller struct {
	client *http.Client
}

type bankName struct {
	ResponseCode string `json:"response_code"`
	AccountName  string `json:"account_name"`
}

func newBankCaller(own string, remote bool) (*bankCaller, error) {
	pool, err := pki.LoadPool(filepath.Join(own, config.NIBSSCAFile))
	if err != nil {
		return nil, err
	}
	certPath, keyPath := filepath.Join(own, "client.crt"), filepath.Join(own, "client.key")
	tc := &tls.Config{
		RootCAs:    pool,
		MinVersion: tls.VersionTLS12,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			pair, err := tls.LoadX509KeyPair(certPath, keyPath)
			if err != nil {
				return nil, fmt.Errorf("load NIBSS client certificate: %w", err)
			}
			return &pair, nil
		},
	}
	dialer := &net.Dialer{Timeout: bankDialTimeout}
	tr := &http.Transport{
		TLSClientConfig:     tc,
		IdleConnTimeout:     bankIdleTimeout,
		MaxIdleConnsPerHost: bankIdleConns,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err == nil && !remote && strings.HasSuffix(host, localhostSuffix) {
				addr = net.JoinHostPort(loopbackHost, port)
			}
			return dialer.DialContext(ctx, network, addr)
		},
	}
	return &bankCaller{client: &http.Client{Timeout: bankCallTimeout, Transport: tr}}, nil
}

func (c *bankCaller) nameAt(ctx context.Context, m nibss.Member, nuban string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(m.CreditURL, "/")+bankNamesPath+nuban, nil)
	if err != nil {
		return "", fmt.Errorf("build name enquiry for %s: %w", m.ID, err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ask %s for a name: %w", m.ID, errors.Join(nip.ErrUnavailable, err))
	}
	defer resp.Body.Close()
	if err := checkBankCertificate(resp, m); err != nil {
		return "", err
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBankBody+1))
	if err != nil {
		return "", fmt.Errorf("read the name from %s: %w", m.ID, errors.Join(nip.ErrUnavailable, err))
	}
	if len(raw) > maxBankBody {
		return "", fmt.Errorf("the name from %s is larger than %d KiB", m.ID, maxBankBody>>10)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s answered %d: %w", m.ID, resp.StatusCode, nip.ErrUnavailable)
	}
	var out bankName
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decode the name from %s: %w", m.ID, err)
	}
	switch out.ResponseCode {
	case nip.OK:
		return out.AccountName, nil
	case nip.InvalidAccount:
		return "", fmt.Errorf("%s: %w", m.ID, nip.ErrInvalidAccount)
	case nip.NoRecord:
		return "", fmt.Errorf("%s: %w", m.ID, nip.ErrNoRecord)
	case nip.Unavailable:
		return "", fmt.Errorf("%s: %w", m.ID, nip.ErrUnavailable)
	}
	return "", fmt.Errorf("%s answered response code %q", m.ID, out.ResponseCode)
}

func checkBankCertificate(resp *http.Response, m nibss.Member) error {
	if resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 {
		return fmt.Errorf("%s did not present a certificate", m.ID)
	}
	if got, want := resp.TLS.PeerCertificates[0].Subject.CommonName, config.BankHost(m.CertificateIdentity); got != want {
		return fmt.Errorf("%s presented the certificate of %s, not %s", m.ID, got, want)
	}
	return nil
}
