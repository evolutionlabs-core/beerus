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
	"path/filepath"
	"strconv"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/pki"
)

const (
	cbnCallTimeout = 10 * time.Second
	cbnIdleTimeout = 90 * time.Second
	maxCBNBody     = 64 << 10
	cbnSessionsURL = "/v1/settlement-sessions"
)

type cbnCaller struct {
	client *http.Client
	base   string
}

type CBNError struct {
	Status  int
	Code    string
	Message string
}

func (e *CBNError) Error() string {
	return fmt.Sprintf("the CBN answered %d %s: %s", e.Status, e.Code, e.Message)
}

func (e *CBNError) Retryable() bool {
	return e.Status == http.StatusConflict || e.Status == http.StatusServiceUnavailable || e.Code == "unknown_bank"
}

type sessionPost struct {
	SessionID string          `json:"session_id"`
	CutoffAt  time.Time       `json:"cutoff_at"`
	Nets      []nibss.BankNet `json:"nets"`
}

func newCBNCaller(own string, port int, remote bool) (*cbnCaller, error) {
	pool, err := pki.LoadPool(filepath.Join(own, config.NIBSSCAFile))
	if err != nil {
		return nil, err
	}
	certPath, keyPath := filepath.Join(own, "client.crt"), filepath.Join(own, "client.key")
	tc := &tls.Config{
		RootCAs:    pool,
		ServerName: config.CBNHost,
		MinVersion: tls.VersionTLS12,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 || cs.PeerCertificates[0].Subject.CommonName != config.CBNHost {
				return errors.New("the server presented another certificate identity than the CBN")
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
	tr := &http.Transport{TLSClientConfig: tc, IdleConnTimeout: cbnIdleTimeout, MaxIdleConnsPerHost: 1, DialContext: localDial(remote)}
	redirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &cbnCaller{client: &http.Client{Timeout: cbnCallTimeout, Transport: tr, CheckRedirect: redirect}, base: "https://" + net.JoinHostPort(config.CBNHost, strconv.Itoa(port))}, nil
}

func (c *cbnCaller) post(ctx context.Context, key string, cutoff time.Time, nets []nibss.BankNet) error {
	raw, err := json.Marshal(sessionPost{SessionID: key, CutoffAt: cutoff, Nets: nets})
	if err != nil {
		return fmt.Errorf("encode settlement %s: %w", key, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+cbnSessionsURL, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("build settlement %s: %w", key, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("post settlement %s to the CBN: %w", key, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCBNBody+1))
	if err != nil {
		return fmt.Errorf("read the CBN answer to settlement %s: %w", key, err)
	}
	if len(body) > maxCBNBody {
		return fmt.Errorf("the CBN answer to settlement %s is larger than %d KiB", key, maxCBNBody>>10)
	}
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		return fmt.Errorf("settlement %s: %w", key, &CBNError{Status: resp.StatusCode, Code: "unreadable", Message: resp.Status})
	}
	return fmt.Errorf("settlement %s: %w", key, &CBNError{Status: resp.StatusCode, Code: e.Error.Code, Message: e.Error.Message})
}
