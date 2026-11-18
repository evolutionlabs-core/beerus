package connect

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

const RoleCore = "core"

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
	var peer *server.PeerError
	if errors.As(err, &peer) && peer.Status == http.StatusNotFound {
		return openbanking.ErrNotFound
	}
	return err
}
