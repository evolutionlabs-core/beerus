package server

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	MaxPeerBody     = 16 << 20
	peerTimeout     = 30 * time.Second
	peerIdleTimeout = 90 * time.Second
)

type Link struct {
	Client *http.Client
	Token  string
	Bind   string
}

type PeerError struct {
	Status  int
	Code    string
	Message string
}

func (e *PeerError) Error() string {
	return e.Message
}

func NewLink(token, bind string) Link {
	return Link{Client: &http.Client{Timeout: peerTimeout, Transport: &http.Transport{IdleConnTimeout: peerIdleTimeout, MaxIdleConnsPerHost: 2}}, Token: token, Bind: bind}
}

func (l Link) Multi() bool {
	return l.Bind != ""
}

func (l Link) Remote() bool {
	ip := net.ParseIP(l.Bind)
	return l.Multi() && (ip == nil || !ip.IsLoopback())
}

func (l Link) Allowed(r *http.Request) bool {
	if l.Remote() {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	return err == nil && net.ParseIP(host).IsLoopback()
}

func (l Link) TokenOK(r *http.Request) bool {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && subtle.ConstantTimeCompare([]byte(tok), []byte(l.Token)) == 1
}

func (l Link) Do(ctx context.Context, base, method, path string, in, out any) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, peerTimeout)
	defer cancel()
	return l.Request(ctx, base, method, path, nil, in, out)
}

func (l Link) Request(ctx context.Context, base, method, path string, header http.Header, in, out any) (int, error) {
	var rd io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(base, "/")+path, rd)
	if err != nil {
		return 0, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Authorization", "Bearer "+l.Token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := l.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxPeerBody+1))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("reading the answer to %s: %w", path, err)
	}
	if len(raw) > MaxPeerBody {
		return resp.StatusCode, fmt.Errorf("the answer to %s is larger than %d MiB", path, MaxPeerBody>>20)
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		msg := e.Error.Message
		if msg == "" {
			msg = resp.Status
		}
		return resp.StatusCode, &PeerError{Status: resp.StatusCode, Code: e.Error.Code, Message: msg}
	}
	if out != nil && len(raw) > 0 {
		return resp.StatusCode, json.Unmarshal(raw, out)
	}
	return resp.StatusCode, nil
}
