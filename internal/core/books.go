package core

import (
	"context"
	"fmt"

	"github.com/evolutionlabs-core/beerus/internal/config"
)

func (a *App) booksOf(id string) bankBooks {
	if !a.booksReady(id) {
		return nil
	}
	c, err := a.adminOf(id)
	if err != nil {
		return nil
	}
	return c
}

func (a *App) booksReady(id string) bool {
	a.lifeMu.Lock()
	defer a.lifeMu.Unlock()
	st := a.status[id]
	return st != nil && st.Phase != PhaseClosed && st.Steps[stepIndex(config.StepOpenBankingSchema)].Done
}

func (a *App) VerifyBank(ctx context.Context) (Check, error) {
	var out Check
	for _, b := range a.bankList() {
		books := a.booksOf(b.ID)
		if books == nil {
			if a.bankPhase(b.ID) != PhaseClosed && !a.bankRetired(b.ID) {
				return out, &bankNotReadyError{Name: b.Name}
			}
			continue
		}
		c, err := books.check(ctx)
		if err != nil {
			return out, fmt.Errorf("verify the books of %s: %w", b.ID, err)
		}
		out.TrialBalance += c.TrialBalance
		out.BalanceMismatch = append(out.BalanceMismatch, c.BalanceMismatch...)
		out.EventMismatch = append(out.EventMismatch, c.EventMismatch...)
		out.UnbalancedEntries = append(out.UnbalancedEntries, c.UnbalancedEntries...)
	}
	return out, nil
}
