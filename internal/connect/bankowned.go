package connect

import (
	"strconv"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/state"
)

func bankOwned(b *config.Bank) []setup.Owned {
	list := []setup.Owned{{Kind: state.OwnedDatabase, Name: b.Database}, {Kind: state.OwnedPort, Name: strconv.Itoa(b.Port)}}
	if b.APIPort != 0 {
		list = append(list, setup.Owned{Kind: state.OwnedPort, Name: strconv.Itoa(b.APIPort)})
	}
	return list
}

func (a *App) claimBank(b *config.Bank) error {
	return a.ctl.Claim(setup.KindBank, b.ID, bankOwned(b)...)
}
