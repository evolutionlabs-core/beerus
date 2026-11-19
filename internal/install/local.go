package install

import (
	"os"
	"path/filepath"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func FreshLocal(dir string) (setup.Local, error) {
	var local setup.Local
	found, err := readYAML(filepath.Join(dir, setup.LocalFile), &local)
	if err != nil {
		return setup.Local{}, err
	}
	if !found {
		local = setup.DefaultLocal()
		if u := os.Getenv(config.DatabaseURLEnv); u != "" {
			local.DatabaseURL = u
		}
		return local, nil
	}
	return local, nil
}
