package aggregator

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

const (
	connectKeyID     = "connect-1"
	registryRecheck  = 30 * time.Second
	registryWait     = "Connect is still registering with NIBSS. Try again in a minute."
	nibssAway        = "NIBSS is not available right now. Try again soon."
	nibssClientsPath = "/v1/control/registry/clients"
)

type nibssAdmin struct {
	control.NIBSSControl
}

type clientBody struct {
	Name         string          `json:"name"`
	LegalName    string          `json:"legal_name"`
	CAC          string          `json:"cac"`
	Role         string          `json:"role"`
	CertCN       string          `json:"cert_cn"`
	JWK          openbanking.JWK `json:"jwk"`
	RedirectURIs []string        `json:"redirect_uris"`
}

func (n nibssAdmin) ensureClient(ctx context.Context, c openbanking.RegistryClient) (openbanking.RegistryClient, error) {
	var out openbanking.RegistryClient
	body := clientBody{Name: c.Name, LegalName: c.LegalName, CAC: c.CAC, Role: c.Role, CertCN: c.CertCN, JWK: c.JWK, RedirectURIs: c.RedirectURIs}
	return out, n.Call(ctx, http.MethodPut, nibssClientsPath+"/"+url.PathEscape(c.ID), nil, body, &out)
}

func (n nibssAdmin) clients(ctx context.Context) ([]openbanking.RegistryClient, error) {
	return control.AllPages(ctx, func(ctx context.Context, cursor string) ([]openbanking.RegistryClient, string, error) {
		var page struct {
			Data       []openbanking.RegistryClient `json:"data"`
			NextCursor string                       `json:"next_cursor"`
		}
		err := n.Call(ctx, http.MethodGet, nibssClientsPath+"?"+control.PageQuery(cursor), nil, nil, &page)
		return page.Data, page.NextCursor, err
	})
}

func (a *App) connectClient() (openbanking.RegistryClient, error) {
	key, err := a.cfg.SigningKey()
	if err != nil {
		return openbanking.RegistryClient{}, err
	}
	conn, callback := a.cfg.Connect, a.cfg.WidgetURL()+"/bank/callback"
	return openbanking.RegistryClient{
		ID:           conn.RegistryID,
		Name:         conn.BrandName,
		LegalName:    conn.LegalName,
		CAC:          conn.CACNumber,
		Role:         config.AggregatorRole,
		CertCN:       config.ConnectID,
		JWK:          openbanking.JWKOf(&key.PublicKey, connectKeyID),
		RedirectURIs: []string{callback},
	}, nil
}

func (a *App) registerDue(ctx context.Context) {
	a.mu.Lock()
	due := !a.clock.Real().Before(a.regNext)
	a.mu.Unlock()
	if !due {
		return
	}
	err := a.ensureConnectClient(ctx)
	now := a.clock.Real()
	a.mu.Lock()
	defer a.mu.Unlock()
	if err == nil {
		a.regReady, a.regAttempts, a.regProblem = true, 0, ""
		a.regNext = now.Add(registryRecheck)
		return
	}
	var peer *control.PeerError
	refused := errors.As(err, &peer) && peer.Status == http.StatusUnprocessableEntity
	if refused {
		a.regReady = false
		if a.regProblem != err.Error() {
			a.regProblem = err.Error()
			a.warnings = append(a.warnings, fmt.Sprintf("NIBSS refuses the registry client of Connect: %v", err))
			a.log.Warn("NIBSS refuses the registry client of Connect", "err", err)
		}
	}
	a.regAttempts++
	a.regNext = now.Add(clock.Backoff(a.regAttempts))
	a.logNIBSS("registry client not sent to NIBSS; retrying", err)
}

func (a *App) logNIBSS(msg string, err error) {
	var down *control.NIBSSDownError
	if errors.As(err, &down) {
		a.log.Warn(msg, "err", err)
		return
	}
	a.log.Error(msg, "err", err)
}

func (a *App) ensureConnectClient(ctx context.Context) error {
	c, err := a.connectClient()
	if err != nil {
		return fmt.Errorf("build the registry client of Connect: %w", err)
	}
	listed, err := a.nibssCtl.clients(ctx)
	if err != nil {
		return fmt.Errorf("read the registry: %w", err)
	}
	for _, old := range listed {
		if old.ID != c.ID {
			continue
		}
		for _, u := range old.RedirectURIs {
			if !slices.Contains(c.RedirectURIs, u) {
				c.RedirectURIs = append(c.RedirectURIs, u)
			}
		}
	}
	if _, err := a.nibssCtl.ensureClient(ctx, c); err != nil {
		return fmt.Errorf("register %s with the registry: %w", c.ID, err)
	}
	return nil
}

func (a *App) registered(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		ready := a.regReady
		a.mu.Unlock()
		if !ready {
			web.WriteError(w, r, http.StatusServiceUnavailable, "not_ready", registryWait)
			return
		}
		next(w, r)
	}
}
