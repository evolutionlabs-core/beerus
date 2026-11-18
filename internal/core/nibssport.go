package core

import (
	"fmt"
	"strconv"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/state"
)

func (a *App) ensureNIBSSPort() error {
	port := 0
	err := a.ctl.Update(func(st *setup.Status) (bool, error) {
		r := state.EnsureResource(st, setup.KindSystem, setup.SystemNIBSS)
		if r.Allocated.Port != 0 {
			port = r.Allocated.Port
			return false, nil
		}
		c := a.cfg.Connect
		used := map[int]bool{c.PartnerPort: true, c.WidgetPort: true, c.ControlPort: true, c.HealthPort: true, c.NIBSSPort: true, c.ConnectControlPort: true}
		for _, x := range st.Resources {
			used[x.Allocated.Port], used[x.Allocated.APIPort] = true, true
		}
		p, err := a.cfg.AllocatePort(a.cfg.Connect.NIBSSPort+1, used, config.PortFree)
		if err != nil {
			return false, fmt.Errorf("NIBSS control port: %w", err)
		}
		r.Allocated.Port = p
		r.Owned = append(r.Owned, setup.Owned{Kind: state.OwnedPort, Name: strconv.Itoa(p)})
		port = p
		return true, nil
	})
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.cfg.Connect.NIBSSControlPort = port
	a.mu.Unlock()
	return nil
}
