package site

import (
	"context"
	"errors"
	"net/http"

	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

func (s *site) revoke(w http.ResponseWriter, r *http.Request) {
	sess := s.user(w, r)
	if sess == nil {
		return
	}
	if err := s.Open.Revoke(r.Context(), r.PathValue("id"), sess.Customer, "bank"); err != nil {
		http.NotFound(w, r)
		return
	}
	ctx := context.WithoutCancel(r.Context())
	s.Sender.Events(ctx)
	s.Sender.Ledger(ctx)
	http.Redirect(w, r, "/apps?revoked=1", http.StatusSeeOther)
}

func (s *site) internalRevoke(w http.ResponseWriter, r *http.Request) {
	if !s.Link.TokenOK(r) {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	var body struct {
		ConsentID string `json:"consent_id"`
	}
	if err := web.DecodeBody(r, &body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	err := s.Open.RevokeByBank(r.Context(), body.ConsentID, "bank")
	if errors.Is(err, openbanking.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.Log.Error("consent not revoked", "consent", body.ConsentID, "err", err)
		http.Error(w, "the consent could not be revoked", http.StatusInternalServerError)
		return
	}
	ctx := context.WithoutCancel(r.Context())
	s.Sender.Events(ctx)
	s.Sender.Ledger(ctx)
	w.WriteHeader(http.StatusNoContent)
}
