package participant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
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

var errCBNAway = errors.New("the CBN did not answer")

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

func newCBNCaller(port int, remote bool) *cbnCaller {
	tr := &http.Transport{IdleConnTimeout: cbnIdleTimeout, MaxIdleConnsPerHost: 1, DialContext: localDial(remote)}
	redirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &cbnCaller{client: &http.Client{Timeout: cbnCallTimeout, Transport: tr, CheckRedirect: redirect}, base: "http://" + net.JoinHostPort(config.CBNHost, strconv.Itoa(port))}
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
	req.Header.Set(config.CallerHeader, config.NIBSSID)
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("post settlement %s: %w: %w", key, errCBNAway, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCBNBody+1))
	if err != nil {
		return fmt.Errorf("read the answer to settlement %s: %w: %w", key, errCBNAway, err)
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
