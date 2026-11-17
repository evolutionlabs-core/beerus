package participant

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

type clientRequest struct {
	Name         string          `json:"name"`
	LegalName    string          `json:"legal_name"`
	CAC          string          `json:"cac"`
	Role         string          `json:"role"`
	CertCN       string          `json:"cert_cn"`
	JWK          openbanking.JWK `json:"jwk"`
	RedirectURIs []string        `json:"redirect_uris"`
}

func (p *Participant) ensureClient(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("client_id")
	var req clientRequest
	if err := web.DecodeBody(r, &req); err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "Send the name, legal_name, cac, role, cert_cn, jwk and redirect_uris of the client.")
		return
	}
	c := openbanking.RegistryClient{ID: id, Name: req.Name, LegalName: req.LegalName, CAC: req.CAC, Role: req.Role, CertCN: req.CertCN, JWK: req.JWK, RedirectURIs: req.RedirectURIs, RegisteredAt: p.clock.Now()}
	out, err := p.store.EnsureClient(r.Context(), c)
	var conflict *nibss.RegistryConflictError
	var bad *nibss.InvalidError
	switch {
	case err == nil:
		web.WriteJSON(w, http.StatusOK, out)
	case errors.As(err, &conflict):
		web.WriteError(w, r, http.StatusUnprocessableEntity, "registration_conflict", web.Sentence(err.Error())+" Send the registered details.")
	case errors.As(err, &bad):
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", web.Sentence(err.Error()))
	default:
		p.unavailable(w, r, "client not registered", err)
	}
}

func (p *Participant) listClients(w http.ResponseWriter, r *http.Request) {
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	data, next, err := p.store.Clients(r.Context(), limit, r.URL.Query().Get("cursor"))
	var cursor *nibss.CursorError
	switch {
	case err == nil:
		web.WriteJSON(w, http.StatusOK, web.ListPage(data, next))
	case errors.As(err, &cursor):
		badCursor(w, r)
	default:
		p.unavailable(w, r, "registry not listed", err)
	}
}

func (p *Participant) bankListClients(w http.ResponseWriter, r *http.Request) {
	if p.admit(w, r, "read the registry") {
		p.listClients(w, r)
	}
}

func (p *Participant) bankClient(w http.ResponseWriter, r *http.Request) {
	if !p.admit(w, r, "read the registry") {
		return
	}
	id := r.PathValue("client_id")
	c, err := p.store.Client(r.Context(), id)
	switch {
	case err == nil:
		web.WriteJSON(w, http.StatusOK, c)
	case errors.Is(err, nibss.ErrUnknownClient):
		web.WriteError(w, r, http.StatusNotFound, "not_found", fmt.Sprintf("Client %s is not in the registry.", id))
	default:
		p.unavailable(w, r, "client not read", err)
	}
}
