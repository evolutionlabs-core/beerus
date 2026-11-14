package server

import (
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
	"strconv"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/pki"
)

const (
	directoryTTL     = 10 * time.Second
	directoryPage    = 500
	directoryTimeout = 5 * time.Second
	directoryIdle    = 90 * time.Second
	registryTTL      = 5 * time.Second
	registryStale    = 3 * registryTTL
)

type BVNRequest struct {
	Name  string `json:"name"`
	Phone string `json:"phone"`
	DOB   string `json:"dob"`
}

type BVNReply struct {
	BVN   string `json:"bvn"`
	Phone string `json:"phone"`
}

type NameEnquiry struct {
	Code        string `json:"response_code,omitempty"`
	Message     string `json:"message"`
	BankCode    string `json:"bank_code"`
	BankName    string `json:"bank_name,omitempty"`
	AccountNo   string `json:"account_number"`
	AccountName string `json:"account_name,omitempty"`
}

type directoryAnswer struct {
	Data       []nip.Institution `json:"data"`
	NextCursor string            `json:"next_cursor"`
}

type directoryError struct {
	status int
	code   string
}

func (e *directoryError) Error() string {
	return fmt.Sprintf("NIBSS answered %d %s", e.status, e.code)
}

func (s *Server) CoreDo(ctx context.Context, method, path string, in, out any) (int, error) {
	return s.link.Do(ctx, s.coreURL, method, path, in, out)
}

func (s *Server) coreAway(err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	return &UnavailableError{Msg: s.Bank().Name + " cannot reach NIBSS right now. Try again in a minute.", Err: err}
}

func (s *Server) Enrol(ctx context.Context, name, phone string, dob clock.Date) (string, string, error) {
	var out BVNReply
	_, err := s.CoreDo(ctx, http.MethodPost, "/internal/bvns", BVNRequest{Name: name, Phone: phone, DOB: dob.String()}, &out)
	var pe *PeerError
	if errors.As(err, &pe) && pe.Status == http.StatusUnprocessableEntity {
		return "", "", fmt.Errorf("%w: %s", ledger.ErrInvalid, pe.Message)
	}
	if errors.As(err, &pe) && pe.Status == http.StatusServiceUnavailable {
		return "", "", &UnavailableError{Msg: pe.Message}
	}
	if err != nil && !errors.As(err, &pe) {
		return "", "", s.coreAway(err)
	}
	if err != nil {
		return "", "", fmt.Errorf("enrol a BVN at NIBSS: %w", err)
	}
	return out.BVN, out.Phone, nil
}

func (s *Server) Enquire(ctx context.Context, rt Route, nuban string) (string, error) {
	var out NameEnquiry
	q := url.Values{"bank_code": {rt.ToCode}, "account_number": {nuban}}
	_, err := s.CoreDo(ctx, http.MethodGet, "/nip/name-enquiry?"+q.Encode(), nil, &out)
	var pe *PeerError
	if err != nil && !errors.As(err, &pe) {
		return "", s.coreAway(err)
	}
	if err != nil {
		return "", err
	}
	if out.Code != nip.OK {
		return "", Refused(http.StatusUnprocessableEntity, out.Code, "name_enquiry_failed", errors.New(out.Message))
	}
	return out.AccountName, nil
}

func (s *Server) Transfer(ctx context.Context, req TransferRequest) (TransferResult, error) {
	body := TransferBody{From: req.From, To: req.To, ToBank: req.ToBank, Amount: fmt.Sprintf("%d.%02d", req.Amount/100, req.Amount%100), Narration: req.Narration}
	var out TransferResult
	status, err := s.CoreDo(ctx, http.MethodPost, "/banks/"+url.PathEscape(req.FromBank)+"/transfers", body, &out)
	var pe *PeerError
	if err != nil && !errors.As(err, &pe) {
		return out, s.coreAway(err)
	}
	if err != nil && status >= http.StatusBadRequest && status < http.StatusInternalServerError {
		return out, Refused(status, nip.SystemError, "transfer_refused", err)
	}
	return out, err
}

func (s *Server) Directory() []*config.Bank {
	s.dirMu.Lock()
	defer s.dirMu.Unlock()
	return s.dirBanks
}

func (s *Server) directoryClient() (*http.Client, error) {
	s.dirMu.Lock()
	have := s.dirClient
	s.dirMu.Unlock()
	if have != nil {
		return have, nil
	}
	cert, err := tls.LoadX509KeyPair(filepath.Join(s.dir, "banks", s.id, "client.crt"), filepath.Join(s.dir, "banks", s.id, "client.key"))
	if err != nil {
		return nil, fmt.Errorf("load bank client certificate: %w", err)
	}
	pool, err := pki.LoadPool(filepath.Join(s.dir, "banks", s.id, config.BankCAFile))
	if err != nil {
		return nil, err
	}
	tc := &tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: pool, ServerName: config.NIBSSHost, MinVersion: tls.VersionTLS12}
	s.dirMu.Lock()
	defer s.dirMu.Unlock()
	if s.dirClient == nil {
		s.dirClient = &http.Client{Timeout: directoryTimeout, Transport: &http.Transport{TLSClientConfig: tc, IdleConnTimeout: directoryIdle, MaxIdleConnsPerHost: 1}}
	}
	return s.dirClient, nil
}

func (s *Server) directoryURL(cursor string) (string, error) {
	core, err := url.Parse(s.coreURL)
	if err != nil || core.Hostname() == "" {
		return "", fmt.Errorf("core URL %q has no host", s.coreURL)
	}
	if s.nibssPort == 0 {
		return "", fmt.Errorf("%s sets no nibss_port", config.BankFilePath(s.dir, s.id))
	}
	q := url.Values{"limit": {strconv.Itoa(directoryPage)}}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	host := net.JoinHostPort(core.Hostname(), strconv.Itoa(s.nibssPort))
	return "https://" + host + "/v1/banks?" + q.Encode(), nil
}

func (s *Server) directoryFetch(ctx context.Context, client *http.Client, cursor string) (directoryAnswer, error) {
	var page directoryAnswer
	target, err := s.directoryURL(cursor)
	if err != nil {
		return page, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return page, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return page, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxPeerBody+1))
	if err != nil {
		return page, fmt.Errorf("read the directory answer: %w", err)
	}
	if len(raw) > MaxPeerBody {
		return page, fmt.Errorf("the directory answer is larger than %d MiB", MaxPeerBody>>20)
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		return page, &directoryError{status: resp.StatusCode, code: e.Error.Code}
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return page, fmt.Errorf("decode the directory answer: %w", err)
	}
	return page, nil
}

func (s *Server) RefreshBanks(ctx context.Context) error {
	s.dirMu.Lock()
	fresh := s.dirBanks != nil && s.clock.Real().Sub(s.dirAt) < directoryTTL
	s.dirMu.Unlock()
	if fresh {
		return nil
	}
	if err := s.dirRefresh.do(ctx, s.clock.Real, s.fetchDirectory); err != nil {
		return s.coreAway(fmt.Errorf("read the NIBSS directory: %w", err))
	}
	return nil
}

func (s *Server) fetchDirectory(ctx context.Context) error {
	client, err := s.directoryClient()
	if err != nil {
		return err
	}
	banks, err := s.directoryPages(ctx, client)
	var bad *directoryError
	if errors.As(err, &bad) && bad.code == "invalid_cursor" {
		banks, err = s.directoryPages(ctx, client)
	}
	if err != nil {
		return err
	}
	s.dirMu.Lock()
	s.dirBanks, s.dirAt = banks, s.clock.Real()
	s.dirMu.Unlock()
	return nil
}

func (s *Server) directoryPages(ctx context.Context, client *http.Client) ([]*config.Bank, error) {
	banks := []*config.Bank{}
	cursor := ""
	for {
		page, err := s.directoryFetch(ctx, client, cursor)
		if err != nil {
			return nil, err
		}
		for _, in := range page.Data {
			banks = append(banks, DirectoryBank(in))
		}
		if page.NextCursor == "" {
			return banks, nil
		}
		cursor = page.NextCursor
	}
}

func DirectoryBank(in nip.Institution) *config.Bank {
	b := &config.Bank{ID: in.ID, Name: in.Name, Type: in.Type, NIPCode: in.NIPCode, NUBANCode: in.NUBANCode}
	switch in.Status {
	case nip.StatusSuspended:
		b.State = "suspended"
	case nip.StatusTerminating:
		b.State = "closed"
	}
	return b
}

func (s *Server) RefreshRegistry(ctx context.Context) error {
	if s.registryFresh() {
		return nil
	}
	err := s.regRefresh.do(ctx, s.clock.Real, s.fetchRegistry)
	if err == nil {
		return nil
	}
	s.regMu.Lock()
	defer s.regMu.Unlock()
	if s.regAt.IsZero() || s.clock.Real().Sub(s.regAt) > registryStale {
		return fmt.Errorf("read the registry: %w", err)
	}
	if msg := err.Error(); s.regWarned != msg {
		s.regWarned = msg
		s.log.Warn("registry is stale; serving the last copy", "bank", s.id, "err", err)
	}
	return nil
}

func (s *Server) registryFresh() bool {
	s.regMu.Lock()
	defer s.regMu.Unlock()
	return !s.regAt.IsZero() && s.clock.Real().Sub(s.regAt) < registryTTL
}

func (s *Server) fetchRegistry(ctx context.Context) error {
	var out struct {
		Data []*openbanking.RegistryClient `json:"data"`
	}
	if _, err := s.link.Request(ctx, s.coreURL, http.MethodGet, "/internal/registry", nil, nil, &out); err != nil {
		return err
	}
	clients := map[string]*openbanking.RegistryClient{}
	for _, c := range out.Data {
		clients[c.ID] = c
	}
	s.regMu.Lock()
	defer s.regMu.Unlock()
	s.regWarned = ""
	s.registry = clients
	s.regAt = s.clock.Real()
	return nil
}

func (s *Server) RegistryClient(id string) (*openbanking.RegistryClient, bool) {
	s.regMu.Lock()
	defer s.regMu.Unlock()
	c := s.registry[id]
	return c, c != nil
}

func (s *Server) RegistryClientByCert(cn string) (*openbanking.RegistryClient, bool) {
	s.regMu.Lock()
	defer s.regMu.Unlock()
	for _, c := range s.registry {
		if c.CertCN == cn {
			return c, true
		}
	}
	return nil, false
}
