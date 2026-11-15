package connect

import (
	"fmt"
	"strconv"

	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func (a *App) ensureNIBSSPort() error {
	port := 0
	err := a.ctl.update(func(st *setup.Status) (bool, error) {
		r := ensureResource(st, setup.KindSystem, setup.SystemNIBSS)
		if r.Allocated.Port != 0 {
			port = r.Allocated.Port
			return false, nil
		}
		c := a.cfg.Connect
		used := map[int]bool{c.PartnerPort: true, c.WidgetPort: true, c.ControlPort: true, c.HealthPort: true, c.NIBSSPort: true}
		for _, x := range st.Resources {
			used[x.Allocated.Port], used[x.Allocated.APIPort] = true, true
		}
		p, err := a.cfg.AllocatePort(a.cfg.Connect.NIBSSPort+1, used, portFree)
		if err != nil {
			return false, fmt.Errorf("NIBSS control port: %w", err)
		}
		r.Allocated.Port = p
		r.Owned = append(r.Owned, setup.Owned{Kind: ownedPort, Name: strconv.Itoa(p)})
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
