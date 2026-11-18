package core

import (
	"net/http"
	"net/url"

	"github.com/evolutionlabs-core/beerus/internal/web"
)

func (a *App) ctlConsents(w http.ResponseWriter, r *http.Request) {
	q := url.Values{}
	for _, k := range []string{"bank", "limit", "cursor"} {
		if v := r.URL.Query().Get(k); v != "" {
			q.Set(k, v)
		}
	}
	page, err := a.nibssCtl.consents(r.Context(), q)
	if err != nil {
		a.nibssFailed(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, web.ListPage(page.Data, page.NextCursor))
}
