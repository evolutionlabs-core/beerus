package install

import (
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
	if err := claimSystem(ctl, cfg, setup.SystemCBN); err != nil {
		return err
	}
	return prepareCBN(ctl, cfg, now)
}
