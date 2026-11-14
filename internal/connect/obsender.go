package connect

import "github.com/evolutionlabs-core/beerus/internal/bank/consent"

func (a *App) newSender() *consent.Sender {
	return &consent.Sender{
		Log:       a.log,
		Clock:     a.clock,
		Dir:       a.cfg.Dir,
		WidgetURL: a.cfg.WidgetURL(),
		CA:        a.cfg.Path("ca", "root.crt"),
		Servers:   a.obServers,
		Bank:      a.bankConfig,
	}
}
