package connect

import (
	"context"
	"crypto/tls"
	"fmt"
	"maps"
	"net"
	"net/http"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

func (a *App) prepareOpenBanking() error {
	a.obs = map[string]*openbanking.Server{}
	return a.cfg.EnsureCertificates(a.clock.Real())
}

func (a *App) obBooks(id string) func() *store.Books {
	return func() *store.Books {
		if l := a.obLedger(id); l != nil {
			return l.Books
		}
		return nil
	}
}

func (a *App) openBanking(id string) *openbanking.Server {
	a.obMu.Lock()
	s := a.obs[id]
	a.obMu.Unlock()
	if s != nil {
		return s
	}
	b := a.cfg.Bank(id)
	if b == nil || a.obLedger(id) == nil {
		return nil
	}
	s, err := server.NewOpenBanking(server.OpenConfig{Bank: *b, Books: a.obBooks(id), Registry: a.bankRegistry(id), Outbox: true, Clock: a.clock})
	if err != nil {
		a.log.Error("open banking not built", "bank", id, "err", err)
		return nil
	}
	a.obMu.Lock()
	if cur := a.obs[id]; cur != nil {
		a.obMu.Unlock()
		return cur
	}
	a.obs[id] = s
	a.obMu.Unlock()
	if err := s.Resume(context.Background()); err != nil {
		a.log.Error("open banking requests not cleared", "bank", id, "err", err)
	}
	return s
}

func (a *App) bankRegistry(id string) *server.RegistryCache {
	a.obMu.Lock()
	defer a.obMu.Unlock()
	if r := a.registries[id]; r != nil {
		return r
	}
	if a.registries == nil {
		a.registries = map[string]*server.RegistryCache{}
	}
	r := server.NewRegistryCache(id, a.clock, a.log, func(ctx context.Context) ([]*openbanking.RegistryClient, error) {
		c, err := a.bankNIBSS(id)
		if err != nil {
			return nil, err
		}
		return c.RegistryClients(ctx)
	})
	a.registries[id] = r
	return r
}

func (a *App) forgetOpenBanking(id string) {
	a.obMu.Lock()
	delete(a.obs, id)
	delete(a.registries, id)
	a.obMu.Unlock()
}

func (a *App) obServers() map[string]*openbanking.Server {
	a.obMu.Lock()
	defer a.obMu.Unlock()
	return maps.Clone(a.obs)
}

func (a *App) startBankAPI(b *config.Bank) (*http.Server, error) {
	if b.APIPort == 0 {
		return nil, fmt.Errorf("%s has no API port", b.ID)
	}
	ob := a.openBanking(b.ID)
	if ob == nil {
		return nil, fmt.Errorf("no ledger for %s", b.ID)
	}
	tc, err := server.OpenAPITLS(a.cfg.Dir, b.ID, a.cfg.Path("ca", "root.crt"))
	if err != nil {
		return nil, err
	}
	ls, err := a.link.Listen(b.APIPort)
	if err != nil {
		return nil, err
	}
	srv := &http.Server{Handler: server.OpenAPI(bankHost{a, b.ID}, ob, a.log), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 2 * time.Minute, ErrorLog: server.ErrorLog(a.log)}
	for _, l := range ls {
		go func(l net.Listener) { _ = srv.Serve(tls.NewListener(l, tc)) }(l)
	}
	return srv, nil
}
