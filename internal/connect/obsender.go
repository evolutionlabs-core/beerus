package connect

import (
	"context"
	"strings"

	"github.com/evolutionlabs-core/beerus/internal/bank/consent"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func (a *App) newSender() *consent.Sender {
	return &consent.Sender{
		Log:       a.log,
		Clock:     a.clock,
		Dir:       a.cfg.Dir,
		WidgetURL: a.cfg.WidgetURL(),
		CA:        a.cfg.Path("ca", "root.crt"),
		Servers:   a.obServers,
		Bank:      a.bankConfig,
		Record:    a.recordConsent,
		Report:    a.noteLedger,
	}
}

func (a *App) recordConsent(ctx context.Context, incarnation string, e openbanking.ConsentEvent) error {
	c, err := a.bankNIBSS(e.Bank)
	if err != nil {
		return err
	}
	return c.RecordConsent(ctx, incarnation, e)
}

func (a *App) noteLedger(bank string, problems []string) {
	note := strings.Join(problems, " ")
	a.mu.Lock()
	same := a.ledgerNotes[bank] == note
	if a.ledgerNotes == nil {
		a.ledgerNotes = map[string]string{}
	}
	a.ledgerNotes[bank] = note
	a.mu.Unlock()
	if same {
		return
	}
	now := a.clock.Real()
	a.recordBank(bank, func(r *setup.Resource) bool {
		if len(problems) == 0 {
			return clearCondition(r, setup.ConditionConsentLedger)
		}
		return setCondition(r, setup.Condition{Type: setup.ConditionConsentLedger, Status: "False", Reason: setup.ReasonRejected, Message: note, ChangedAt: now})
	})
}
