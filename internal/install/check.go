package install

import (
	"fmt"
	"os"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func CheckStart(dir string) error {
	st, found, err := loadSetup(dir)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("no Beerus setup in %s. Run beerus init first.", dir)
	}
	if systemPhase(st, setup.SystemCBN) == config.PhaseProvisioning {
		return fmt.Errorf("the setup in %s is not finished. Run beerus init again to finish it.", dir)
	}
	return nil
}

func loadSetup(dir string) (setup.Status, bool, error) {
	ok, err := setup.Initialized(dir)
	if err != nil || !ok {
		return setup.Status{}, false, err
	}
	st, err := setup.LoadStatus(dir)
	if err != nil {
		return setup.Status{}, false, err
	}
	return st, true, nil
}

func systemPhase(st setup.Status, id string) string {
	if r := setup.FindResource(&st, setup.KindSystem, id); r != nil {
		return r.Phase
	}
	return ""
}

func CheckPorts(c *config.Config) error {
	ports := []int{c.Connect.PartnerPort, c.Connect.WidgetPort, c.Connect.ControlPort, c.Connect.HealthPort, c.Connect.NIBSSPort}
	for _, p := range []int{c.Connect.ConnectControlPort, c.Connect.NIBSSControlPort, c.Connect.CBNControlPort, c.Connect.CBNPort} {
		if p != 0 {
			ports = append(ports, p)
		}
	}
	for _, b := range c.Banks {
		if !b.Closed() {
			ports = append(ports, b.Port, b.APIPort)
		}
	}
	_, err := os.Stat(c.Path("running"))
	running := err == nil
	for _, p := range ports {
		if config.PortFree(p) {
			continue
		}
		if running {
			return fmt.Errorf("beerus is already running for %s; stop it (Ctrl+C in its terminal) before starting it again", c.Dir)
		}
		return fmt.Errorf("port %d is already in use by another program; free it or change it in local.yaml", p)
	}
	return nil
}
