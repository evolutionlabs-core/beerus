package connect

import (
	"context"
	"net/http"

	"github.com/evolutionlabs-core/beerus/internal/bank/consent"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

func (a *App) newSender() *consent.Sender {
	s := &consent.Sender{
		Log:       a.log,
		Clock:     a.clock,
		Dir:       a.cfg.Dir,
		WidgetURL: a.cfg.WidgetURL(),
		CA:        a.rootCertPath(),
		Servers:   a.obServers,
		Bank:      a.bankConfig,
	}
	if a.role == RoleBank {
		s.Core = func(ctx context.Context, e openbanking.ConsentEvent) error {
			_, err := a.srv.CoreDo(ctx, http.MethodPost, "/internal/consents", e, nil)
			return err
		}
	}
	return s
}
