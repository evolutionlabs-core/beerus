package core

import (
	"slices"
	"strconv"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/state"
)

func (a *App) prepareConnect() error {
	port := a.cfg.Connect.ConnectControlPort
	claim := setup.Owned{Kind: state.OwnedPort, Name: strconv.Itoa(port)}
	return a.ctl.Update(func(st *setup.Status) (bool, error) {
		r := state.EnsureResource(st, setup.KindSystem, setup.SystemConnect)
		if r.Allocated.Port == port && r.Phase == config.PhaseActive && slices.Contains(r.Owned, claim) {
			return false, nil
		}
		r.Allocated.Port = port
		r.Phase = config.PhaseActive
		r.Owned = append(slices.DeleteFunc(r.Owned, func(o setup.Owned) bool { return o.Kind == state.OwnedPort }), claim)
		return true, nil
	})
}
