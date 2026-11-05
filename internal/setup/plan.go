package setup

import (
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
)

func Build(desired Spec, st Status, dir []nibss.Institution, today clock.Date) Plan {
	return Plan{Setup: desired.Metadata.Name, Errors: []error{errNotBuilt}}
}
