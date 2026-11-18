package connect

import (
	"context"
	"fmt"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
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

func (a *App) dropCBN(ctx context.Context) error {
	name := config.DatabaseName(a.cfg.Dir, setup.SystemCBN)
	if err := store.DropDatabases(ctx, a.cfg.DatabaseURL, []string{name}); err != nil {
		return fmt.Errorf("drop the CBN database: %w", err)
	}
	if err := a.ctl.Release(setup.Owned{Kind: ownedDatabase, Name: name}); err != nil {
		return err
	}
	return a.ctl.releasePorts(setup.KindSystem, setup.SystemCBN)
}
