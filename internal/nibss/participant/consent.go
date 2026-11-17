package participant

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

type consentRequest struct {
	SourceSeq int64     `json:"source_seq"`
	ConsentID string    `json:"consent_id"`
	ClientID  string    `json:"client_id"`
	PartnerID string    `json:"partner_id"`
	Status    string    `json:"status"`
	Via       string    `json:"via"`
	ChangedAt time.Time `json:"changed_at"`
}

func (p *Participant) recordConsent(w http.ResponseWriter, r *http.Request) {
	bank, ok := p.identify(w, r, "record consents")
	if !ok {
		return
	}
	var req consentRequest
	if err := web.DecodeBody(r, &req); err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "Send the source_seq, consent_id, client_id, partner_id, status, via and changed_at of the consent event.")
		return
	}
	e := nibss.ConsentEvent{Bank: bank, ConsentID: req.ConsentID, ClientID: req.ClientID, PartnerID: req.PartnerID, Status: req.Status, Via: req.Via, ChangedAt: req.ChangedAt, SourceSeq: req.SourceSeq}
	out, replayed, err := p.store.RecordConsent(r.Context(), e)
	var reused *nibss.ConsentReusedError
	var bad *nibss.InvalidError
	switch {
	case err == nil:
		if replayed {
			w.Header().Set("Idempotent-Replayed", "true")
		}
		web.WriteJSON(w, http.StatusOK, out)
	case errors.As(err, &reused):
		web.WriteError(w, r, http.StatusUnprocessableEntity, "idempotency_key_reused", fmt.Sprintf("Source sequence %d of bank %s was first recorded with other %s. Send a new source_seq, or repeat the first request.", reused.SourceSeq, reused.Bank, strings.Join(reused.Differs, " and ")))
	case errors.As(err, &bad):
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", web.Sentence(err.Error()))
	default:
		p.unavailable(w, r, "consent not recorded", err)
	}
}

func (p *Participant) listConsents(w http.ResponseWriter, r *http.Request) {
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	data, next, err := p.store.ConsentLedger(r.Context(), q.Get("bank"), limit, q.Get("cursor"))
	var cursor *nibss.CursorError
	switch {
	case err == nil:
		web.WriteJSON(w, http.StatusOK, web.ListPage(data, next))
	case errors.As(err, &cursor):
		badCursor(w, r)
	default:
		p.unavailable(w, r, "consents not listed", err)
	}
}
