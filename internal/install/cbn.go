package install

import (
	"fmt"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/state"
)

func PrepareCBN(cfg *config.Config, now time.Time) error {
	ctl, err := state.Open(cfg.Dir)
	if err != nil {
		return err
	}
	if err := ctl.Claim(setup.KindSystem, setup.SystemCBN, setup.Owned{Kind: state.OwnedDatabase, Name: config.DatabaseName(cfg.Dir, setup.SystemCBN)}); err != nil {
		return err
	}
	if err := state.EnsureCBNPorts(ctl, cfg); err != nil {
		return err
	}
	if err := cfg.EnsureCBNDir(now); err != nil {
		return fmt.Errorf("ensure the CBN directory: %w", err)
	}
	return nil
}
