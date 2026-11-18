package connect

import (
	"context"
	"fmt"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

func (a *App) cbnDue(ctx context.Context) {
	now := a.clock.Real()
	var jobs []provisionJob
	a.lifeMu.Lock()
	a.mu.Lock()
	for _, b := range a.cfg.Banks {
		st := a.status[b.ID]
		if st != nil && (st.Phase == PhaseProvisioning || st.Phase == PhaseRepairing) && st.Steps[stepIndex(config.StepCerts)].Done && !st.Steps[stepIndex(config.StepCBN)].Done && (st.NextAttemptAt == nil || !now.Before(*st.NextAttemptAt)) {
			jobs = append(jobs, provisionJob{*b, st})
		}
	}
	a.mu.Unlock()
	a.lifeMu.Unlock()
	for _, j := range jobs {
		a.step(j.st, config.StepCBN, func() error { return a.openCBNAccount(ctx, j.b.ID) })
	}
}

func (a *App) openCBNAccount(ctx context.Context, id string) error {
	if err := a.cbnCtl.openAccount(ctx, id, bank.GenesisSettlement, bank.GenesisVault); err != nil {
		return fmt.Errorf("open the CBN settlement account of %s: %w", id, err)
	}
	return nil
}
