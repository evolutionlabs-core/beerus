package participant

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
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
	bankCreditsPath = "/v1/nip/credits"
	localhostSuffix = ".localhost"
	loopbackHost    = "127.0.0.1"
)

type bankCaller struct {
	client *http.Client
}

var responseCodeRe = regexp.MustCompile(`^[0-9]{2}$`)

type creditBody struct {
	SessionID    string    `json:"session_id"`
	FromCode     string    `json:"from_code"`
	FromBankName string    `json:"from_bank_name"`
	FromNUBAN    string    `json:"from_nuban"`
	FromName     string    `json:"from_name"`
	ToNUBAN      string    `json:"to_nuban"`
	Amount       int64     `json:"amount_minor"`
	Narration    string    `json:"narration"`
	CreatedAt    time.Time `json:"created_at"`
}

type bankName struct {
	ResponseCode string `json:"response_code"`
	AccountName  string `json:"account_name"`
}

func newBankCaller(own string, remote bool, timeout time.Duration) (*bankCaller, error) {
	pool, err := pki.LoadPool(filepath.Join(own, config.NIBSSCAFile))
	if err != nil {
		return nil, err
	}
	certPath, keyPath := filepath.Join(own, "client.crt"), filepath.Join(own, "client.key")
	tc := &tls.Config{
		RootCAs:    pool,
		MinVersion: tls.VersionTLS12,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 || cs.PeerCertificates[0].Subject.CommonName != cs.ServerName {
				return fmt.Errorf("bank at %s presented another certificate identity", cs.ServerName)
			}
			return nil
		},
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
	redirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &bankCaller{client: &http.Client{Timeout: timeout, Transport: tr, CheckRedirect: redirect}}, nil
}

func (c *bankCaller) call(ctx context.Context, m nibss.Member, method, path string, in any) (int, []byte, error) {
	u, err := url.Parse(m.CreditURL)
	if err != nil || u.Hostname() != config.BankHost(m.CertificateIdentity) {
		return 0, nil, fmt.Errorf("credit_url of %s is not the host of %s", m.ID, m.CertificateIdentity)
	}
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return 0, nil, fmt.Errorf("encode the call to %s: %w", m.ID, err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(m.CreditURL, "/")+path, body)
	if err != nil {
		return 0, nil, fmt.Errorf("build the call to %s: %w", m.ID, err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("call %s: %w", m.ID, errors.Join(nip.ErrUnavailable, err))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBankBody+1))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read the answer of %s: %w", m.ID, errors.Join(nip.ErrUnavailable, err))
	}
	if len(raw) > maxBankBody {
		return resp.StatusCode, nil, fmt.Errorf("the answer of %s is larger than %d KiB", m.ID, maxBankBody>>10)
	}
	return resp.StatusCode, raw, nil
}

func (c *bankCaller) nameAt(ctx context.Context, m nibss.Member, nuban string) (string, error) {
	status, raw, err := c.call(ctx, m, http.MethodGet, bankNamesPath+nuban, nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("%s answered %d: %w", m.ID, status, nip.ErrUnavailable)
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

func (c *bankCaller) credit(ctx context.Context, m nibss.Member, in creditBody) (string, error) {
	status, raw, err := c.call(ctx, m, http.MethodPost, bankCreditsPath, in)
	if err != nil {
		return "", err
	}
	if status == http.StatusServiceUnavailable {
		return nip.Unavailable, nil
	}
	return creditCode(m, status, raw)
}

func (c *bankCaller) creditStatus(ctx context.Context, m nibss.Member, sessionID string) (string, error) {
	status, raw, err := c.call(ctx, m, http.MethodGet, bankCreditsPath+"/"+sessionID, nil)
	if err != nil {
		return "", err
	}
	return creditCode(m, status, raw)
}

func creditCode(m nibss.Member, status int, raw []byte) (string, error) {
	if status != http.StatusOK {
		return "", fmt.Errorf("%s answered %d: %w", m.ID, status, nip.ErrUnavailable)
	}
	var out struct {
		ResponseCode string `json:"response_code"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decode the credit answer of %s: %w", m.ID, err)
	}
	if !responseCodeRe.MatchString(out.ResponseCode) {
		return "", fmt.Errorf("%s answered response code %q", m.ID, out.ResponseCode)
	}
	return out.ResponseCode, nil
}
