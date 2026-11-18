package install

import (
	"fmt"
	"path/filepath"

	"go.yaml.in/yaml/v3"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

const (
	defaultDatabaseURL  = "postgres:///postgres"
	defaultPartnerPort  = 7443
	defaultWidgetPort   = 7444
	defaultControlPort  = 7480
	defaultHealthPort   = 7490
	defaultSyncInterval = "1m"
	defaultRefreshEvery = "1m"
)

func DefaultLocal() setup.Local {
	return setup.Local{
		DatabaseURL: defaultDatabaseURL,
		Connect: setup.LocalConnect{
			Host:         setup.DefaultConnectHost,
			PartnerPort:  defaultPartnerPort,
			WidgetPort:   defaultWidgetPort,
			ControlPort:  defaultControlPort,
			HealthPort:   defaultHealthPort,
			NIBSSPort:    setup.DefaultNIBSSPort,
			CBNPort:      setup.DefaultCBNPort,
			SyncInterval: defaultSyncInterval,
			RefreshEvery: defaultRefreshEvery,
		},
	}
}

func FreshLocal(dir string) (setup.Local, error) {
	var local setup.Local
	found, err := readYAML(filepath.Join(dir, setup.LocalFile), &local)
	if err != nil {
		return setup.Local{}, err
	}
	if !found {
		return DefaultLocal(), nil
	}
	return local, nil
}

func writeYAML(path string, v any) error {
	raw, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode %s: %w", filepath.Base(path), err)
	}
	if err := config.WriteSecret(path, string(raw)); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	return nil
}
