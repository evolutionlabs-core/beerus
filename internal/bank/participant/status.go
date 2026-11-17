package participant

import (
	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

func provisioned(b *config.Bank) (string, []server.Step) {
	steps := []server.Step{}
	for _, s := range config.BankSteps() {
		name := s.Name
		if s.ID == config.StepAllocate && b.NIPCode != "" {
			name += " (NIP " + b.NIPCode + ", NUBAN " + b.NUBANCode + ")"
		}
		steps = append(steps, server.Step{ID: s.ID, Name: name, Done: true})
	}
	if b.Closed() {
		return config.PhaseClosed, steps
	}
	return b.RunningPhase(), steps
}
