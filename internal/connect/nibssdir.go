package connect

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nibss/participant"
)

func (a *App) ensureNIBSSDir() error {
	now := a.clock.Real()
	if !a.nibssEnsured.IsZero() && now.Sub(a.nibssEnsured) < bankEnsureEvery {
		return nil
	}
	a.mu.Lock()
	world := a.st.WorldID
	a.mu.Unlock()
	if err := a.cfg.EnsureNIBSSDir(world, now); err != nil {
		return fmt.Errorf("ensure the NIBSS directory: %w", err)
	}
	a.nibssEnsured = now
	return nil
}

func (a *App) prepareNIBSS() error {
	if err := a.ensureNIBSSPort(); err != nil {
		return err
	}
	if err := a.ensureNIBSSDir(); err != nil {
		return err
	}
	token, err := config.ReadSecret(filepath.Join(config.NIBSSDir(a.cfg.Dir), config.NIBSSTokenFile))
	if err != nil {
		return fmt.Errorf("NIBSS control token: %w", err)
	}
	port := a.cfg.Connect.NIBSSControlPort
	a.nibssCtl = &nibssAdmin{link: server.NewLink(token, ""), base: "http://127.0.0.1:" + strconv.Itoa(port), port: port}
	return nil
}

func (a *App) serveNIBSS(ctx context.Context, errc chan<- error) (<-chan struct{}, error) {
	done := make(chan struct{})
	if a.role == RoleCore {
		close(done)
		return done, nil
	}
	p, err := participant.New(ctx, a.cfg.Dir, participant.Options{Logger: a.log, DatabaseURL: a.cfg.DatabaseURL})
	if err != nil {
		return nil, err
	}
	go func() {
		defer close(done)
		if err := p.Serve(ctx); err != nil {
			errc <- fmt.Errorf("serve NIBSS: %w", err)
		}
	}()
	return done, nil
}
