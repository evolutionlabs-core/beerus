package connect

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

const registryTTL = 5 * time.Second

func (a *App) ctlInternalRegistry(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"data": a.RegistryClients()})
}

func (a *App) refreshRegistry(ctx context.Context) error {
	if a.role != RoleBank {
		return nil
	}
	a.regMu.Lock()
	loaded, fresh := !a.regAt.IsZero(), a.clock.Real().Sub(a.regAt) < registryTTL
	a.regMu.Unlock()
	if fresh && loaded {
		return nil
	}
	var out struct {
		Data []*RegistryClient `json:"data"`
	}
	_, err := a.peerRequest(ctx, a.coreURL, http.MethodGet, "/internal/registry", nil, nil, &out)
	a.regMu.Lock()
	defer a.regMu.Unlock()
	if err != nil {
		if a.regAt.IsZero() {
			return fmt.Errorf("read the registry: %w", err)
		}
		if msg := err.Error(); a.regWarned != msg {
			a.regWarned = msg
			a.log.Warn("registry is stale; serving the last copy", "bank", a.bankID, "err", err)
		}
		return nil
	}
	a.regWarned = ""
	a.st.Registry = map[string]*RegistryClient{}
	for _, c := range out.Data {
		a.st.Registry[c.ID] = c
	}
	a.regAt = a.clock.Real()
	return nil
}
