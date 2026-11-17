package connect

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

const (
	connectKeyID    = "connect-1"
	registryRecheck = 30 * time.Second
	registryWait    = "Connect is still registering with NIBSS. Try again in a minute."
)

var errRegistry = errors.New("registry")

type registryRequest struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	LegalName    string   `json:"legal_name"`
	CAC          string   `json:"cac"`
	CertCN       string   `json:"cert_cn"`
	PublicKey    string   `json:"public_key_pem"`
	RedirectURIs []string `json:"redirect_uris"`
}

func (a *App) addRegistryClient(ctx context.Context, req registryRequest) (openbanking.RegistryClient, error) {
	block, _ := pem.Decode([]byte(req.PublicKey))
	if block == nil {
		return openbanking.RegistryClient{}, fmt.Errorf("%w: public_key_pem must hold a PEM public key", errRegistry)
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return openbanking.RegistryClient{}, fmt.Errorf("%w: the public key could not be read", errRegistry)
	}
	ec, ok := pub.(*ecdsa.PublicKey)
	if !ok || ec.Curve != elliptic.P256() {
		return openbanking.RegistryClient{}, fmt.Errorf("%w: the public key must be an EC P-256 key", errRegistry)
	}
	if req.Name == "" {
		req.Name = req.ID
	}
	if req.LegalName == "" {
		req.LegalName = req.Name
	}
	c := openbanking.RegistryClient{ID: req.ID, Name: req.Name, LegalName: req.LegalName, CAC: req.CAC, Role: config.AggregatorRole, CertCN: req.CertCN, JWK: openbanking.JWKOf(ec, req.ID), RedirectURIs: req.RedirectURIs}
	return a.nibssCtl.ensureClient(ctx, c, true)
}

func (a *App) connectClient() (openbanking.RegistryClient, error) {
	key, err := a.cfg.SigningKey()
	if err != nil {
		return openbanking.RegistryClient{}, err
	}
	a.mu.Lock()
	conn, callback := a.cfg.Connect, a.cfg.WidgetURL()+"/bank/callback"
	a.mu.Unlock()
	return openbanking.RegistryClient{
		ID:           conn.RegistryID,
		Name:         conn.BrandName,
		LegalName:    conn.LegalName,
		CAC:          conn.CACNumber,
		Role:         config.AggregatorRole,
		CertCN:       config.AggregatorCN,
		JWK:          openbanking.JWKOf(&key.PublicKey, connectKeyID),
		RedirectURIs: []string{callback},
	}, nil
}

func (a *App) registryDue(ctx context.Context) {
	a.mu.Lock()
	due := a.nibssCtl != nil && !a.clock.Real().Before(a.regNext)
	a.mu.Unlock()
	if !due {
		return
	}
	err := a.ensureConnectClient(ctx)
	now := a.clock.Real()
	a.mu.Lock()
	if err == nil {
		a.regReady, a.regAttempts, a.regProblem = true, 0, ""
		a.regNext = now.Add(registryRecheck)
		a.mu.Unlock()
		return
	}
	var peer *server.PeerError
	refused := errors.As(err, &peer) && peer.Status == http.StatusUnprocessableEntity
	changed := refused && a.regProblem != err.Error()
	if refused {
		a.regReady = false
		a.regProblem = err.Error()
	}
	a.regAttempts++
	a.regNext = now.Add(clock.Backoff(a.regAttempts))
	a.mu.Unlock()
	if changed {
		a.lifeMu.Lock()
		a.warn("NIBSS refuses the registry client of Connect: %v", err)
		a.lifeMu.Unlock()
	}
	a.logNIBSS("registry client not sent to NIBSS; retrying", err)
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
	if _, err := a.nibssCtl.ensureClient(ctx, c, false); err != nil {
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

func (a *App) nibssFailed(w http.ResponseWriter, r *http.Request, err error) {
	var peer *server.PeerError
	if errors.As(err, &peer) && peer.Status != http.StatusServiceUnavailable {
		web.WriteError(w, r, peer.Status, peer.Code, peer.Message)
		return
	}
	a.log.ErrorContext(r.Context(), "NIBSS call failed", "err", err)
	web.WriteError(w, r, http.StatusServiceUnavailable, "unavailable", nibssAway)
}

func (a *App) ctlRegistryClients(w http.ResponseWriter, r *http.Request) {
	list, err := a.nibssCtl.clients(r.Context())
	if err != nil {
		a.nibssFailed(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (a *App) ctlRegistryAdd(w http.ResponseWriter, r *http.Request) {
	var req registryRequest
	if err := web.DecodeBody(r, &req); err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "Send a JSON body with id, cert_cn, public_key_pem and redirect_uris.")
		return
	}
	c, err := a.addRegistryClient(r.Context(), req)
	if errors.Is(err, errRegistry) {
		web.WriteError(w, r, http.StatusUnprocessableEntity, "invalid_request", err.Error())
		return
	}
	if err != nil {
		a.nibssFailed(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusCreated, c)
}
