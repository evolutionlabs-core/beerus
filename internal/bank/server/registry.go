package server

import (
	"context"
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

const (
	registryTTL   = 5 * time.Second
	registryStale = 3 * registryTTL
	registryPage  = 500
	registryPath  = "/v1/registry/clients"
)

type registryPageBody struct {
	Data       []*openbanking.RegistryClient `json:"data"`
	NextCursor string                        `json:"next_cursor"`
}

func (c NIBSSClient) RegistryClients(ctx context.Context) ([]*openbanking.RegistryClient, error) {
	out, err := control.AllPages(ctx, func(ctx context.Context, cursor string) ([]*openbanking.RegistryClient, string, error) {
		q := url.Values{"limit": {strconv.Itoa(registryPage)}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var page registryPageBody
		_, err := c.Do(ctx, http.MethodGet, registryPath, q, nil, &page)
		return page.Data, page.NextCursor, err
	})
	if err != nil {
		return nil, fmt.Errorf("read the registry at NIBSS: %w", err)
	}
	return out, nil
}

type RegistryCache struct {
	bank  string
	clock *clock.Clock
	log   *slog.Logger
	fetch func(ctx context.Context) ([]*openbanking.RegistryClient, error)

	flight  refresher
	mu      sync.Mutex
	clients map[string]*openbanking.RegistryClient
	at      time.Time
	warned  string
}

func NewRegistryCache(bank string, c *clock.Clock, log *slog.Logger, fetch func(ctx context.Context) ([]*openbanking.RegistryClient, error)) *RegistryCache {
	return &RegistryCache{bank: bank, clock: c, log: log, fetch: fetch, clients: map[string]*openbanking.RegistryClient{}}
}

func (r *RegistryCache) Refresh(ctx context.Context) error {
	if r.fresh() {
		return nil
	}
	err := r.flight.do(ctx, r.clock, r.load)
	if err == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.at.IsZero() || r.clock.Real().Sub(r.at) > registryStale {
		return fmt.Errorf("read the registry: %w", err)
	}
	if msg := err.Error(); r.warned != msg {
		r.warned = msg
		r.log.Warn("registry is stale; serving the last copy", "bank", r.bank, "err", err)
	}
	return nil
}

func (r *RegistryCache) fresh() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.at.IsZero() && r.clock.Real().Sub(r.at) < registryTTL
}

func (r *RegistryCache) load(ctx context.Context) error {
	if r.fresh() {
		return nil
	}
	list, err := r.fetch(ctx)
	if err != nil {
		return err
	}
	clients := map[string]*openbanking.RegistryClient{}
	for _, c := range list {
		clients[c.ID] = c
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.warned = ""
	r.clients = clients
	r.at = r.clock.Real()
	return nil
}

func (r *RegistryCache) RegistryClient(id string) (*openbanking.RegistryClient, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.clients[id]
	return c, c != nil
}

func (r *RegistryCache) RegistryClientByCaller(id string) (*openbanking.RegistryClient, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.clients {
		if c.CertCN == id {
			return c, true
		}
	}
	return nil, false
}

func (s *Server) fetchRegistry(ctx context.Context) ([]*openbanking.RegistryClient, error) {
	c, err := s.NIBSS()
	if err != nil {
		return nil, err
	}
	return c.RegistryClients(ctx)
}

func (s *Server) RefreshRegistry(ctx context.Context) error {
	return s.reg.Refresh(ctx)
}

func (s *Server) RegistryClient(id string) (*openbanking.RegistryClient, bool) {
	return s.reg.RegistryClient(id)
}

func (s *Server) RegistryClientByCaller(id string) (*openbanking.RegistryClient, bool) {
	return s.reg.RegistryClientByCaller(id)
}
