package setup

import "github.com/evolutionlabs-core/beerus/internal/clock"

func Export(s Spec) ([]byte, error) {
	return nil, errNotBuilt
}

func DefaultSpec(name string, today clock.Date) Spec {
	return Spec{}
}
