package connect

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func PrepareCBN(cfg *config.Config, now time.Time) error {
	ctl, err := openStore(cfg.Dir)
	if err != nil {
		return err
	}
	if err := ctl.Claim(setup.KindSystem, setup.SystemCBN, setup.Owned{Kind: ownedDatabase, Name: config.DatabaseName(cfg.Dir, setup.SystemCBN)}); err != nil {
		return err
	}
	if err := ensureCBNPorts(ctl, cfg); err != nil {
		return err
	}
	if err := cfg.EnsureCBNDir(now); err != nil {
		return fmt.Errorf("ensure the CBN directory: %w", err)
	}
	return nil
}

func (a *App) prepareCBN() error {
	if err := a.ctl.Claim(setup.KindSystem, setup.SystemCBN, setup.Owned{Kind: ownedDatabase, Name: config.DatabaseName(a.cfg.Dir, setup.SystemCBN)}); err != nil {
		return err
	}
	if err := ensureCBNPorts(a.ctl, a.cfg); err != nil {
		return err
	}
	token, err := config.ReadSecret(filepath.Join(config.CBNDir(a.cfg.Dir), config.CBNTokenFile))
	if err != nil {
		return fmt.Errorf("CBN control token: %w", err)
	}
	port := a.cfg.Connect.CBNControlPort
	a.cbnCtl = &cbnAdmin{link: server.NewLink(token, a.link.Bind), base: a.cfg.CBNURL(), port: port}
	return nil
}
