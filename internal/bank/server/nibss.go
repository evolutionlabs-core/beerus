package server

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/nip"
)

const (
	nibssBVNsPath      = "/v1/bvns"
	nibssEnquiriesPath = "/v1/name-enquiries"
)

const ControlEnquiriesPath = "/v1/control/name-enquiries"

type NIBSSClient struct {
	HTTP          *http.Client
	Base          string
	Away          string
	Token         string
	EnquiriesPath string
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
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return 0, err
		}
		return 0, &UnavailableError{Msg: c.Away, Err: err}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxPeerBody+1))
	if err != nil {
		return resp.StatusCode, &UnavailableError{Msg: c.Away, Err: fmt.Errorf("read the answer to %s: %w", path, err)}
	}
	if len(raw) > MaxPeerBody {
		return resp.StatusCode, fmt.Errorf("the answer to %s is larger than %d MiB", path, MaxPeerBody>>20)
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
	pe := &PeerError{Status: resp.StatusCode, Code: e.Error.Code, Message: msg}
	if resp.StatusCode == http.StatusServiceUnavailable {
		return &UnavailableError{Msg: msg, Err: pe}
	}
	return pe
}

func (c NIBSSClient) Enrol(ctx context.Context, name, phone string, dob clock.Date) (nip.BVNReply, error) {
	var out nip.BVNReply
	_, err := c.Do(ctx, http.MethodPost, nibssBVNsPath, nil, nip.BVNRequest{Name: name, Phone: phone, DOB: dob.String()}, &out)
	var pe *PeerError
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
	_, err := c.Do(ctx, http.MethodGet, cmp.Or(c.EnquiriesPath, nibssEnquiriesPath), q, nil, &out)
	if err != nil {
		return out, fmt.Errorf("ask NIBSS for a name: %w", err)
	}
	return out, nil
}

func (s *Server) nibss() (NIBSSClient, error) {
	hc, err := s.directoryClient()
	if err != nil {
		return NIBSSClient{}, s.coreAway(err)
	}
	base, err := s.nibssBase()
	if err != nil {
		return NIBSSClient{}, s.coreAway(err)
	}
	return NIBSSClient{HTTP: hc, Base: base, Away: s.Bank().Name + " cannot reach NIBSS right now. Try again in a minute."}, nil
}

func (s *Server) Enrol(ctx context.Context, _, name, phone string, dob clock.Date) (string, string, error) {
	c, err := s.nibss()
	if err != nil {
		return "", "", err
	}
	out, err := c.Enrol(ctx, name, phone, dob)
	if err != nil {
		return "", "", err
	}
	return out.BVN, out.Phone, nil
}

func (s *Server) Enquire(ctx context.Context, rt Route, nuban string) (string, error) {
	c, err := s.nibss()
	if err != nil {
		return "", err
	}
	out, err := c.Enquire(ctx, rt.ToCode, nuban)
	if err != nil {
		return "", err
	}
	if out.Code != nip.OK {
		return "", Refused(http.StatusUnprocessableEntity, out.Code, "name_enquiry_failed", errors.New(out.Message))
	}
	return out.AccountName, nil
}
