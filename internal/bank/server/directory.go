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

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/pki"
)

const (
	directoryTTL     = 10 * time.Second
	directoryPage    = 500
	directoryTimeout = 5 * time.Second
	directoryIdle    = 90 * time.Second
)

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
			banks = append(banks, directoryBank(in))
		}
		if page.NextCursor == "" {
			return banks, nil
		}
		cursor = page.NextCursor
	}
}

func directoryBank(in nip.Institution) *config.Bank {
	b := &config.Bank{ID: in.ID, Name: in.Name, Type: in.Type, NIPCode: in.NIPCode, NUBANCode: in.NUBANCode}
	switch in.Status {
	case nip.StatusSuspended:
		b.State = "suspended"
	case nip.StatusTerminating:
		b.State = "closed"
	}
	return b
}
