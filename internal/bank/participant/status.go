package participant

import (
	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

func initialSteps(b *config.Bank) []server.Step {
	steps := []server.Step{}
	for _, s := range config.BankSteps() {
		name := s.Name
		if s.ID == config.StepAllocate && b.NIPCode != "" {
			name += " (NIP " + b.NIPCode + ", NUBAN " + b.NUBANCode + ")"
		}
		steps = append(steps, server.Step{ID: s.ID, Name: name, Done: !ensured(s.ID)})
	}
	return steps
}

func ensured(id string) bool {
	switch id {
	case config.StepDatabase, config.StepLedgerSchema, config.StepOpenBankingSchema:
		return true
	}
	return false
}

func runningPhase(b *config.Bank) string {
	if b.Closed() {
		return config.PhaseClosed
	}
	return b.RunningPhase()
}

func stepName(id string) string {
	for _, s := range config.BankSteps() {
		if s.ID == id {
			return s.Name
		}
	}
	return id
}
