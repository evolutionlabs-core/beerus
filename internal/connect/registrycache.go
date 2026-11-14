package connect

import (
	"context"
	"net/http"

	"github.com/evolutionlabs-core/beerus/internal/web"
)

func (a *App) ctlInternalRegistry(w http.ResponseWriter, r *http.Request) {
	web.WriteJSON(w, http.StatusOK, map[string]any{"data": a.RegistryClients()})
}

func (a *App) refreshRegistry(ctx context.Context) error {
	if a.srv == nil {
		return nil
	}
	return a.srv.RefreshRegistry(ctx)
}

func (a *App) refreshBanks(ctx context.Context) error {
	if a.srv == nil {
		return nil
	}
	return a.srv.RefreshBanks(ctx)
}
