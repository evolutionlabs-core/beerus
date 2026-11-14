package connect

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

const (
	registryTTL   = 5 * time.Second
	registryStale = 3 * registryTTL
)

func (a *App) ctlInternalRegistry(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"data": a.RegistryClients()})
}

func (a *App) refreshRegistry(ctx context.Context) error {
	if a.role != RoleBank {
		return nil
	}
	if a.registryFresh() {
		return nil
	}
	err := a.regRefresh.do(ctx, a.clock.Real, a.fetchRegistry)
	if err == nil {
		return nil
	}
	a.regMu.Lock()
	defer a.regMu.Unlock()
	if a.regAt.IsZero() || a.clock.Real().Sub(a.regAt) > registryStale {
		return fmt.Errorf("read the registry: %w", err)
	}
	if msg := err.Error(); a.regWarned != msg {
		a.regWarned = msg
		a.log.Warn("registry is stale; serving the last copy", "bank", a.bankID, "err", err)
	}
	return nil
}

func (a *App) registryFresh() bool {
	a.regMu.Lock()
	defer a.regMu.Unlock()
	return !a.regAt.IsZero() && a.clock.Real().Sub(a.regAt) < registryTTL
}

func (a *App) fetchRegistry(ctx context.Context) error {
	var out struct {
		Data []*RegistryClient `json:"data"`
	}
	if _, err := a.peerRequest(ctx, a.coreURL, http.MethodGet, "/internal/registry", nil, nil, &out); err != nil {
		return err
	}
	clients := map[string]*RegistryClient{}
	for _, c := range out.Data {
		clients[c.ID] = c
	}
	a.regMu.Lock()
	defer a.regMu.Unlock()
	a.regWarned = ""
	a.st.Registry = clients
	a.regAt = a.clock.Real()
	return nil
}
