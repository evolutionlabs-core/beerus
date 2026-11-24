package server

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

const (
	unavailableBody = `{"error":"temporarily_unavailable","error_description":"The bank is not available right now."}`
	retryAfter      = "30"
)

func jsonStatus(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func unavailable(w http.ResponseWriter) {
	w.Header().Set("Retry-After", retryAfter)
	jsonStatus(w, http.StatusServiceUnavailable, unavailableBody)
}

func OpenAPI(h Host, ob *openbanking.Server, log *slog.Logger) http.Handler {
	id := h.Bank().ID
	api := ob.Handler()
	guarded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if phase, _ := h.Phase(); phase == config.PhaseClosed {
			jsonStatus(w, http.StatusConflict, `{"error":"bank_closed","error_description":`+strconv.Quote(h.Bank().Name+" is closed.")+`}`)
			return
		}
		if _, err := h.Books(); err != nil || h.Refusing(id) {
			unavailable(w)
			return
		}
		if err := h.RefreshRegistry(r.Context()); err != nil {
			log.Error("registry not readable", "bank", id, "err", err)
			unavailable(w)
			return
		}
		api.ServeHTTP(w, r)
	})
	mux := http.NewServeMux()
	mux.Handle("/v1/nip/", h.Banking().NIP(id))
	mux.Handle("/", guarded)
	return mux
}
