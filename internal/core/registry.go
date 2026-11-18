package core

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"net/http"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/web"
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
	return a.nibssCtl.ensureClient(ctx, c)
}

func (a *App) nibssFailed(w http.ResponseWriter, r *http.Request, err error) {
	var peer *control.PeerError
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
