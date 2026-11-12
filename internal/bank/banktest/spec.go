package banktest

import (
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func Spec(name string, today clock.Date) setup.Spec {
	s := setup.DefaultSpec(name, today)
	s.Spec.Banks = []setup.Bank{
		{ID: "bank-a", Name: "Bank A", Type: "commercial"},
		{ID: "bank-b", Name: "Bank B", Type: "digital"},
	}
	return s
}
