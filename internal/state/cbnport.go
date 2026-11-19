package state

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func EnsureCBNPorts(ctl *Store, cfg *config.Config) error {
	control := 0
	err := ctl.Update(func(st *setup.Status) (bool, error) {
		r := EnsureResource(st, setup.KindSystem, setup.SystemCBN)
		if r.Allocated.Port != 0 && r.Allocated.APIPort == cfg.Connect.CBNPort {
			control = r.Allocated.Port
			return false, nil
		}
		if r.Allocated.Port == 0 {
			c := cfg.Connect
			used := map[int]bool{c.PartnerPort: true, c.WidgetPort: true, c.ControlPort: true, c.HealthPort: true, c.NIBSSPort: true, c.CBNPort: true, c.ConnectControlPort: true}
			for _, x := range st.Resources {
				used[x.Allocated.Port], used[x.Allocated.APIPort] = true, true
			}
			p, err := cfg.AllocatePort(c.CBNPort+1, used, config.PortFree)
			if err != nil {
				return false, fmt.Errorf("CBN control port: %w", err)
			}
			r.Allocated.Port = p
			r.Owned = append(r.Owned, setup.Owned{Kind: OwnedPort, Name: strconv.Itoa(p)})
		}
		if r.Allocated.APIPort != cfg.Connect.CBNPort {
			old := setup.Owned{Kind: OwnedPort, Name: strconv.Itoa(r.Allocated.APIPort)}
			r.Owned = slices.DeleteFunc(r.Owned, func(x setup.Owned) bool { return x == old })
			r.Allocated.APIPort = cfg.Connect.CBNPort
			r.Owned = append(r.Owned, setup.Owned{Kind: OwnedPort, Name: strconv.Itoa(cfg.Connect.CBNPort)})
		}
		control = r.Allocated.Port
		return true, nil
	})
	if err != nil {
		return err
	}
	cfg.Connect.CBNControlPort = control
	return nil
}
