package participant

import (
	"errors"
	"net/http"

	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

func (p *Participant) settlementReports(w http.ResponseWriter, r *http.Request) {
	identity, ok := p.identify(w, r, "read settlement reports")
	if !ok {
		return
	}
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	data, next, err := p.store.SettlementReports(r.Context(), identity, q.Get("after"), limit, q.Get("cursor"))
	var cursor *nibss.CursorError
	switch {
	case err == nil:
		web.WriteJSON(w, http.StatusOK, web.ListPage(data, next))
	case errors.As(err, &cursor):
		badCursor(w, r)
	default:
		p.unavailable(w, r, "settlement reports not listed", err)
	}
}
