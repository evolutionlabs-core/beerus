package core

import (
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"path/filepath"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/state"
)

func (a *App) ensureNIBSSDir() error {
	now := a.clock.Real()
	if ensuredRecently(a.nibssEnsured, now) {
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
	if err := a.ctl.Claim(setup.KindSystem, setup.SystemNIBSS, setup.Owned{Kind: state.OwnedDatabase, Name: config.DatabaseName(a.cfg.Dir, setup.SystemNIBSS)}); err != nil {
		return err
	}
	if err := state.MarkActive(a.ctl, setup.SystemNIBSS); err != nil {
		return err
	}
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
	a.nibssCtl = &nibssAdmin{control.NewNIBSSControl(control.NewLink(token, ""), port)}
	return nil
}
