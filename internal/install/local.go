package install

import (
	"path/filepath"

	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func FreshLocal(dir string) (setup.Local, error) {
	var local setup.Local
	found, err := readYAML(filepath.Join(dir, setup.LocalFile), &local)
	if err != nil {
		return setup.Local{}, err
	}
	if !found {
		return setup.DefaultLocal(), nil
	}
	return local, nil
}
