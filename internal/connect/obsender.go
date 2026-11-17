package connect

import (
	"context"

	"github.com/evolutionlabs-core/beerus/internal/bank/consent"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
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
	}
}

func (a *App) recordConsent(ctx context.Context, bank string, e openbanking.ConsentEvent) error {
	c, err := a.bankNIBSS(bank)
	if err != nil {
		return err
	}
	return c.RecordConsent(ctx, e)
}
