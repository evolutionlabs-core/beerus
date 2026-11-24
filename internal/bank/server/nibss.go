package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nip"
)

const (
	nibssBVNsPath      = "/v1/bvns"
	nibssEnquiriesPath = "/v1/name-enquiries"
)

type NIBSSClient struct {
	HTTP *http.Client
	Base string
	Away string
	ID   string
}

func (c NIBSSClient) Do(ctx context.Context, method, path string, query url.Values, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, fmt.Errorf("encode the request to %s: %w", path, err)
		}
		body = bytes.NewReader(b)
	}
	target := strings.TrimSuffix(c.Base, "/") + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return 0, fmt.Errorf("build the request to %s: %w", path, err)
	}
	req.Header.Set(config.CallerHeader, c.ID)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return 0, err
		}
		return 0, &control.UnavailableError{Msg: c.Away, Err: err}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, control.MaxPeerBody+1))
	if err != nil {
		return resp.StatusCode, &control.UnavailableError{Msg: c.Away, Err: fmt.Errorf("read the answer to %s: %w", path, err)}
	}
	if len(raw) > control.MaxPeerBody {
		return resp.StatusCode, fmt.Errorf("the answer to %s is larger than %d MiB", path, control.MaxPeerBody>>20)
	}
	if resp.StatusCode >= http.StatusMultipleChoices {
		return resp.StatusCode, peerFailure(resp, raw)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode the answer to %s: %w", path, err)
		}
	}
	return resp.StatusCode, nil
}

func peerFailure(resp *http.Response, raw []byte) error {
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
	pe := &control.PeerError{Status: resp.StatusCode, Code: e.Error.Code, Message: msg}
	if resp.StatusCode == http.StatusServiceUnavailable {
		return &control.UnavailableError{Msg: msg, Err: pe}
	}
	return pe
}

func (c NIBSSClient) Enrol(ctx context.Context, name, phone string, dob clock.Date) (nip.BVNReply, error) {
	var out nip.BVNReply
	_, err := c.Do(ctx, http.MethodPost, nibssBVNsPath, nil, nip.BVNRequest{Name: name, Phone: phone, DOB: dob.String()}, &out)
	var pe *control.PeerError
	if errors.As(err, &pe) && pe.Status == http.StatusUnprocessableEntity {
		return out, fmt.Errorf("%w: %s", ledger.ErrInvalid, pe.Message)
	}
	if err != nil {
		return out, fmt.Errorf("enrol a BVN at NIBSS: %w", err)
	}
	return out, nil
}

func (c NIBSSClient) Enquire(ctx context.Context, code, nuban string) (nip.NameEnquiry, error) {
	var out nip.NameEnquiry
	q := url.Values{"bank_code": {code}, "account_number": {nuban}}
	_, err := c.Do(ctx, http.MethodGet, nibssEnquiriesPath, q, nil, &out)
	if err != nil {
		return out, fmt.Errorf("ask NIBSS for a name: %w", err)
	}
	return out, nil
}

func (s *Server) NIBSS() (NIBSSClient, error) {
	base, err := s.nibssBase()
	if err != nil {
		return NIBSSClient{}, s.coreAway(err)
	}
	return NIBSSClient{HTTP: s.dirClient, Base: base, Away: s.Bank().Name + " cannot reach NIBSS right now. Try again in a minute.", ID: s.id}, nil
}

func (s *Server) coreAway(err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	return &control.UnavailableError{Msg: s.Bank().Name + " cannot reach NIBSS right now. Try again in a minute.", Err: err}
}

func (s *Server) Enrol(ctx context.Context, name, phone string, dob clock.Date) (string, string, error) {
	c, err := s.NIBSS()
	if err != nil {
		return "", "", err
	}
	out, err := c.Enrol(ctx, name, phone, dob)
	if err != nil {
		return "", "", err
	}
	return out.BVN, out.Phone, nil
}
