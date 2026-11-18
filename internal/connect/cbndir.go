package connect

import (
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"path/filepath"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/state"
)

func (a *App) prepareCBN() error {
	if err := a.ctl.Claim(setup.KindSystem, setup.SystemCBN, setup.Owned{Kind: state.OwnedDatabase, Name: config.DatabaseName(a.cfg.Dir, setup.SystemCBN)}); err != nil {
		return err
	}
	if err := state.EnsureCBNPorts(a.ctl, a.cfg); err != nil {
		return err
	}
	token, err := config.ReadSecret(filepath.Join(config.CBNDir(a.cfg.Dir), config.CBNTokenFile))
	if err != nil {
		return fmt.Errorf("CBN control token: %w", err)
	}
	port := a.cfg.Connect.CBNControlPort
	a.cbnCtl = &cbnAdmin{link: control.NewLink(token, a.link.Bind), base: a.cfg.CBNURL(), port: port}
	return nil
}
