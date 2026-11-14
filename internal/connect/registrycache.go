package connect

import (
	"net/http"

	"github.com/evolutionlabs-core/beerus/internal/web"
)

func (a *App) ctlInternalRegistry(w http.ResponseWriter, r *http.Request) {
	web.WriteJSON(w, http.StatusOK, map[string]any{"data": a.RegistryClients()})
}
