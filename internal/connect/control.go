package connect

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/evolutionlabs-core/beerus/internal/web"
)

func (a *App) ControlHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		web.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok", "world_id": a.WorldID()})
	})
	mux.HandleFunc("GET /nip/name-enquiry", a.ctlNameEnquiry)
	mux.HandleFunc("GET /nip/participants", a.ctlNIPParticipants)
	mux.HandleFunc("GET /nip/positions", a.ctlNIPPositions)
	mux.HandleFunc("GET /nip/settlement", a.ctlNIPSettlement)
	mux.HandleFunc("POST /nip/settle-now", a.ctlSettleNow)
	mux.HandleFunc("GET /nip/sessions/{id}", a.ctlNIPSession)
	mux.HandleFunc("POST /nip/sessions/{id}/status", a.ctlNIPSessionStatus)
	mux.HandleFunc("GET /faults", a.ctlFaults)
	mux.HandleFunc("POST /faults", a.ctlAddFault)
	mux.HandleFunc("DELETE /faults", a.ctlClearFaults)
	mux.HandleFunc("POST /banks/{id}/net-debit-cap", a.ctlNetDebitCap)
	mux.HandleFunc("GET /connections", a.ctlConnections)
	mux.HandleFunc("POST /connections/{id}/revoke", a.ctlRevokeConnection)
	mux.HandleFunc("POST /plan", a.ctlPlan)
	mux.HandleFunc("POST /apply", a.ctlApply)
	mux.HandleFunc("GET /resources", a.ctlResources)
	mux.HandleFunc("GET /resources/{kind}/{id}", a.ctlResource)
	mux.HandleFunc("GET /closures/{id}", a.ctlClosure)
	mux.HandleFunc("POST /partners", a.ctlAddPartner)
	mux.HandleFunc("POST /partners/{id}/rotate-key", a.ctlRotatePartner)
	mux.HandleFunc("DELETE /partners/{id}", a.ctlRemovePartner)
	mux.HandleFunc("GET /registry/clients", a.ctlRegistryClients)
	mux.HandleFunc("POST /registry/clients", a.ctlRegistryAdd)
	mux.HandleFunc("GET /registry/consents", a.ctlConsents)
	mux.HandleFunc("GET /webhooks/deliveries", a.ctlDeliveries)
	mux.HandleFunc("POST /webhooks/deliveries/{id}/replay", a.ctlReplay)
	mux.HandleFunc("GET /banks", func(w http.ResponseWriter, r *http.Request) {
		data := []BankStatus{}
		for _, st := range a.BankStatuses() {
			if state := r.URL.Query().Get("state"); state == "" || st.Phase == state {
				data = append(data, st)
			}
		}
		web.WriteJSON(w, http.StatusOK, map[string]any{"data": data, "warnings": a.BankWarnings()})
	})
	mux.HandleFunc("POST /banks", a.ctlCreateBank)
	mux.HandleFunc("GET /banks/{id}/customers", a.atBank)
	mux.HandleFunc("POST /banks/{id}/customers", a.atBank)
	mux.HandleFunc("POST /banks/{id}/customers/{customer_id}/close", a.atBank)
	mux.HandleFunc("GET /banks/{id}/accounts", a.atBank)
	mux.HandleFunc("POST /banks/{id}/accounts", a.atBank)
	mux.HandleFunc("POST /banks/{id}/accounts/{nuban}/close", a.atBank)
	mux.HandleFunc("GET /banks/{id}/accounts/{nuban}/statement", a.atBank)
	mux.HandleFunc("POST /banks/{id}/deposits", a.atBankSyncing)
	mux.HandleFunc("POST /banks/{id}/withdrawals", a.atBankSyncing)
	mux.HandleFunc("POST /banks/{id}/transfers", a.atBankSyncing)
	mux.HandleFunc("POST /banks/{id}/transfers/quote", a.atBank)
	mux.HandleFunc("GET /banks/{id}/close", a.ctlCloseImpact)
	mux.HandleFunc("POST /banks/{id}/close", a.ctlCloseBank)
	mux.HandleFunc("GET /banks/{id}/purge", a.ctlPurgeImpact)
	mux.HandleFunc("POST /banks/{id}/purge", a.ctlPurgeBank)
	mux.HandleFunc("POST /banks/{id}/suspend", func(w http.ResponseWriter, r *http.Request) { a.ctlSuspend(w, r, true) })
	mux.HandleFunc("POST /banks/{id}/resume", func(w http.ResponseWriter, r *http.Request) { a.ctlSuspend(w, r, false) })
	mux.HandleFunc("POST /sync", a.ctlSync)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		web.WriteError(w, r, http.StatusNotFound, "not_found", "No such endpoint.")
	})
	api := web.WithRequestID(a.localOnly(mux))
	room := a.roomHandler()
	top := http.NewServeMux()
	top.Handle("/ui", room)
	top.Handle("/ui/", room)
	top.Handle("/assets/", room)
	top.Handle("/", api)
	return top
}

func (a *App) localOnly(next http.Handler) http.Handler {
	port := strconv.Itoa(a.cfg.Connect.ControlPort)
	hosts := map[string]bool{"127.0.0.1:" + port: true, "localhost:" + port: true, "[::1]:" + port: true}
	if u, err := url.Parse(a.coreURL); err == nil && a.link.Multi() && u.Host != "" {
		hosts[u.Host] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.link.Allowed(r) {
			web.WriteError(w, r, http.StatusForbidden, "forbidden", "The control API only accepts local connections.")
			return
		}
		if !hosts[r.Host] {
			web.WriteError(w, r, http.StatusForbidden, "forbidden", "Unexpected Host header.")
			return
		}
		if o := r.Header.Get("Origin"); o != "" {
			web.WriteError(w, r, http.StatusForbidden, "forbidden", "Browser requests are refused.")
			return
		}
		if !a.link.TokenOK(r) {
			web.WriteError(w, r, http.StatusUnauthorized, "unauthenticated", "A valid control token is required.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) WorldID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.st.WorldID
}
