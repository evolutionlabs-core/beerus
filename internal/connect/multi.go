package connect

import (
	"context"
	"net/http"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

const RoleCore = "core"

func (a *App) ctlConsentRecord(w http.ResponseWriter, r *http.Request) {
	var e openbanking.ConsentEvent
	if err := web.DecodeBody(r, &e); err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "Send a consent event.")
		return
	}
	consentLedger{a}.Record(e)
	w.WriteHeader(http.StatusNoContent)
}

const probeTimeout = 5 * time.Second

func (a *App) bankCopy(id string) (config.Bank, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.cfg.Bank(id)
	if b == nil {
		return config.Bank{}, false
	}
	return *b, true
}

func (a *App) revokeRemote(ctx context.Context, b *config.Bank, consent string) error {
	_, err := a.link.Do(ctx, b.URL(), http.MethodPost, "/internal/revoke", map[string]string{"consent_id": consent}, nil)
	return err
}
