package participant

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

func (p *Participant) controlSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("session_id")
	x, err := p.store.Session(r.Context(), id)
	switch {
	case err == nil:
		web.WriteJSON(w, http.StatusOK, x)
	case errors.Is(err, nip.ErrNoRecord):
		web.WriteError(w, r, http.StatusNotFound, "not_found", fmt.Sprintf("Session %s is not in the NIBSS records.", id))
	default:
		p.unavailable(w, r, "session not read", err)
	}
}

func (p *Participant) closureSessions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("bank_id")
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	m, err := p.store.Member(r.Context(), id)
	if errors.Is(err, nip.ErrUnknownInstitution) {
		unknownBank(w, r, id)
		return
	}
	if err != nil {
		p.unavailable(w, r, "bank not read", err)
		return
	}
	data, next, err := p.store.ClosureSessions(r.Context(), m.NIPCode, limit, r.URL.Query().Get("cursor"))
	var cursor *nibss.CursorError
	switch {
	case err == nil:
		web.WriteJSON(w, http.StatusOK, web.ListPage(data, next))
	case errors.As(err, &cursor):
		badCursor(w, r)
	default:
		p.unavailable(w, r, "closure sessions not listed", err)
	}
}
