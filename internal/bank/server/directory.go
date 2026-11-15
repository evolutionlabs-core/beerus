package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
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

func (s *Server) Directory() []*config.Bank {
	s.dirMu.Lock()
	defer s.dirMu.Unlock()
	return s.dirBanks
}

func OpenBanks(all []*config.Bank) []*config.Bank {
	out := []*config.Bank{}
	for _, b := range all {
		if !b.Closed() {
			out = append(out, b)
		}
	}
	return out
}

func (s *Server) directoryClient() (*http.Client, error) {
	s.dirMu.Lock()
	have := s.dirClient
	s.dirMu.Unlock()
	if have != nil {
		return have, nil
	}
	built, err := NIBSSHTTP(s.dir, s.id)
	if err != nil {
		return nil, err
	}
	s.dirMu.Lock()
	defer s.dirMu.Unlock()
	if s.dirClient == nil {
		s.dirClient = built
	}
	return s.dirClient, nil
}

func NIBSSHTTP(dir, id string) (*http.Client, error) {
	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, "banks", id, "client.crt"), filepath.Join(dir, "banks", id, "client.key"))
	if err != nil {
		return nil, fmt.Errorf("load bank client certificate: %w", err)
	}
	pool, err := pki.LoadPool(filepath.Join(dir, "banks", id, config.BankCAFile))
	if err != nil {
		return nil, err
	}
	tc := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   config.NIBSSHost,
		MinVersion:   tls.VersionTLS12,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 || cs.PeerCertificates[0].Subject.CommonName != config.NIBSSHost {
				return errors.New("the server presented another certificate identity than NIBSS")
			}
			return nil
		},
	}
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &http.Client{Timeout: directoryTimeout, CheckRedirect: noRedirect, Transport: &http.Transport{TLSClientConfig: tc, IdleConnTimeout: directoryIdle, MaxIdleConnsPerHost: 1}}, nil
}

func (s *Server) nibssBase() (string, error) {
	core, err := url.Parse(s.coreURL)
	if err != nil || core.Hostname() == "" {
		return "", fmt.Errorf("core URL %q has no host", s.coreURL)
	}
	if s.nibssPort == 0 {
		return "", fmt.Errorf("%s sets no nibss_port", config.BankFilePath(s.dir, s.id))
	}
	return "https://" + net.JoinHostPort(core.Hostname(), strconv.Itoa(s.nibssPort)), nil
}

func (s *Server) directoryFetch(ctx context.Context, c NIBSSClient, cursor string) (directoryAnswer, error) {
	var page directoryAnswer
	q := url.Values{"limit": {strconv.Itoa(directoryPage)}}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	_, err := c.Do(ctx, http.MethodGet, "/v1/banks", q, nil, &page)
	return page, err
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
	c, err := s.nibss()
	if err != nil {
		return err
	}
	banks, err := s.directoryPages(ctx, c)
	var bad *PeerError
	if errors.As(err, &bad) && bad.Code == "invalid_cursor" {
		banks, err = s.directoryPages(ctx, c)
	}
	if err != nil {
		return err
	}
	s.dirMu.Lock()
	s.dirBanks, s.dirAt = banks, s.clock.Real()
	s.dirMu.Unlock()
	return nil
}

func (s *Server) directoryPages(ctx context.Context, c NIBSSClient) ([]*config.Bank, error) {
	banks := []*config.Bank{}
	cursor := ""
	for {
		page, err := s.directoryFetch(ctx, c, cursor)
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
