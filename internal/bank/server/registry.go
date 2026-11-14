package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

const (
	registryTTL   = 5 * time.Second
	registryStale = 3 * registryTTL
)

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
