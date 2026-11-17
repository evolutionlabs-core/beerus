package connect

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
)

func (a *App) ControlHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "world_id": a.WorldID()})
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
	mux.HandleFunc("GET /internal/replica", a.ctlReplica)
	mux.HandleFunc("POST /internal/consents", a.ctlConsentRecord)
	mux.HandleFunc("POST /banks/{id}/net-debit-cap", a.ctlNetDebitCap)
	mux.HandleFunc("GET /connections", a.ctlConnections)
	mux.HandleFunc("POST /connections/{id}/revoke", func(w http.ResponseWriter, r *http.Request) {
		if err := a.RevokeAtBank(r.PathValue("id")); err != nil {
			writeError(w, r, http.StatusNotFound, "not_found", "No such connection.")
			return
		}
		a.poke()
		writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
	})
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
	mux.HandleFunc("GET /registry/consents", a.ctlConsentLedger)
	mux.HandleFunc("GET /webhooks/deliveries", a.ctlDeliveries)
	mux.HandleFunc("POST /webhooks/deliveries/{id}/replay", func(w http.ResponseWriter, r *http.Request) {
		if err := a.Replay(r.PathValue("id")); err != nil {
			writeError(w, r, http.StatusNotFound, "not_found", "No such delivery.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "pending"})
	})
	mux.HandleFunc("GET /banks", func(w http.ResponseWriter, r *http.Request) {
		data := []BankStatus{}
		for _, st := range a.BankStatuses() {
			if state := r.URL.Query().Get("state"); state == "" || st.Phase == state {
				data = append(data, st)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data, "warnings": a.BankWarnings()})
	})
	mux.HandleFunc("POST /banks", a.ctlCreateBank)
	mux.HandleFunc("GET /banks/{id}/customers", a.ctlCustomers)
	mux.HandleFunc("POST /banks/{id}/customers", a.ctlCreateCustomer)
	mux.HandleFunc("POST /banks/{id}/customers/{customer}/close", a.ctlCloseCustomer)
	mux.HandleFunc("GET /banks/{id}/accounts", a.ctlAccounts)
	mux.HandleFunc("POST /banks/{id}/accounts", a.ctlOpenAccount)
	mux.HandleFunc("POST /banks/{id}/accounts/{nuban}/close", a.ctlCloseAccount)
	mux.HandleFunc("GET /banks/{id}/accounts/{nuban}/statement", a.ctlStatement)
	mux.HandleFunc("POST /banks/{id}/deposits", a.ctlCash("deposit"))
	mux.HandleFunc("POST /banks/{id}/withdrawals", a.ctlCash("withdrawal"))
	mux.HandleFunc("POST /banks/{id}/transfers", a.ctlBankTransfer)
	mux.HandleFunc("POST /banks/{id}/transfers/quote", a.ctlBankQuote)
	mux.HandleFunc("GET /banks/{id}/close", a.ctlCloseImpact)
	mux.HandleFunc("POST /banks/{id}/close", a.ctlCloseBank)
	mux.HandleFunc("GET /banks/{id}/purge", a.ctlPurgeImpact)
	mux.HandleFunc("POST /banks/{id}/purge", a.ctlPurgeBank)
	mux.HandleFunc("POST /banks/{id}/suspend", func(w http.ResponseWriter, r *http.Request) { a.ctlSuspend(w, r, true) })
	mux.HandleFunc("POST /banks/{id}/resume", func(w http.ResponseWriter, r *http.Request) { a.ctlSuspend(w, r, false) })
	mux.HandleFunc("POST /sync", func(w http.ResponseWriter, r *http.Request) {
		a.SyncAll(r.Context())
		writeJSON(w, http.StatusOK, map[string]string{"status": "synced"})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusNotFound, "not_found", "No such endpoint.")
	})
	api := a.withRequestID(a.localOnly(mux))
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
	if u, err := url.Parse(a.coreURL); err == nil && a.multiProcess() && u.Host != "" {
		hosts[u.Host] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if host, _, err := net.SplitHostPort(r.RemoteAddr); !a.remotePeers() && (err != nil || !net.ParseIP(host).IsLoopback()) {
			writeError(w, r, http.StatusForbidden, "forbidden", "The control API only accepts local connections.")
			return
		}
		if !hosts[r.Host] {
			writeError(w, r, http.StatusForbidden, "forbidden", "Unexpected Host header.")
			return
		}
		if o := r.Header.Get("Origin"); o != "" {
			writeError(w, r, http.StatusForbidden, "forbidden", "Browser requests are refused.")
			return
		}
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(tok), []byte(a.ctlToken)) != 1 {
			writeError(w, r, http.StatusUnauthorized, "unauthenticated", "A valid control token is required.")
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

func parseNaira(s string) (int64, error) {
	whole, frac, _ := strings.Cut(strings.ReplaceAll(strings.TrimSpace(s), ",", ""), ".")
	if whole == "" || len(frac) > 2 || strings.Trim(whole, "0123456789") != "" || strings.Trim(frac, "0123456789") != "" {
		return 0, fmt.Errorf("amount %q must look like 25000 or 25000.50 (naira)", s)
	}
	for len(frac) < 2 {
		frac += "0"
	}
	n, err := strconv.ParseInt(whole+frac, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("amount %q must be a positive naira amount", s)
	}
	return n, nil
}

func naira(minor int64) string {
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	n := strconv.FormatInt(minor/100, 10)
	out := ""
	for i, c := range n {
		if i > 0 && (len(n)-i)%3 == 0 {
			out += ","
		}
		out += string(c)
	}
	return fmt.Sprintf("%s₦%s.%02d", sign, out, minor%100)
}

func (a *App) ctlConnections(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []map[string]any{}
	for _, c := range a.st.Connections {
		m := a.connectionJSON(c)
		m["partner_id"] = c.PartnerID
		m["customer_id"] = c.CustomerID
		m["account_ids"] = c.AccountIDs
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["created_at"].(string) < out[j]["created_at"].(string) })
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

const (
	deliveryPageDefault = 50
	deliveryPageMax     = 500
)

func (a *App) ctlDeliveries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := deliveryPageDefault
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > deliveryPageMax {
			writeError(w, r, http.StatusBadRequest, "invalid_request", fmt.Sprintf("limit must be a number from 1 to %d.", deliveryPageMax))
			return
		}
		limit = n
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	from := len(a.st.Deliveries) - 1
	if cursor := q.Get("cursor"); cursor != "" {
		from = slices.IndexFunc(a.st.Deliveries, func(d *Delivery) bool { return d.ID == cursor }) - 1
		if from < -1 {
			writeError(w, r, http.StatusBadRequest, "invalid_cursor", "That cursor is not valid. Use the next_cursor value from the last page.")
			return
		}
	}
	out := []map[string]any{}
	next := ""
	for i := from; i >= 0; i-- {
		d := a.st.Deliveries[i]
		if len(out) == limit {
			next = out[len(out)-1]["id"].(string)
			break
		}
		out = append(out, map[string]any{"id": d.ID, "type": d.Type, "status": d.Status, "attempts": d.Attempts, "last_status": d.LastStatus, "last_error": d.LastError, "body": d.Body})
	}
	writeJSON(w, http.StatusOK, listPage(out, next))
}

func (a *App) SyncAll(ctx context.Context) {
	if a.role == RoleBank {
		return
	}
	a.mu.Lock()
	now := a.clock.Now()
	due := make([]*Connection, 0, len(a.st.Connections))
	for _, c := range a.st.Connections {
		if c.Status == "active" || c.Status == "syncing" || c.Status == "institution_down" {
			due = append(due, c)
		}
	}
	a.mu.Unlock()
	for _, c := range due {
		a.syncConnection(c, now)
	}
	a.Step(ctx)
}
