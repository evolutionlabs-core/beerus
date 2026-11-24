package aggregator

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strconv"

	"github.com/evolutionlabs-core/beerus/internal/control"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

const (
	pageDefault = control.ConnectPageSize
	pageMax     = control.ConnectPageMax
)

func (a *App) ControlHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", a.ctlHealth)
	mux.HandleFunc("GET /v1/status", a.ctlStatus)
	mux.HandleFunc("GET /v1/control/connections", a.ctlConnections)
	mux.HandleFunc("POST /v1/control/connections/{id}/revoke", a.ctlRevoke)
	mux.HandleFunc("POST /v1/control/banks/{id}/revoke-connections", a.ctlRevokeScope(a.RevokeBank))
	mux.HandleFunc("POST /v1/control/partners/{id}/revoke-connections", a.ctlRevokeScope(a.RevokePartner))
	mux.HandleFunc("DELETE /v1/control/banks/{id}", a.ctlDrop(a.dropBankRows))
	mux.HandleFunc("DELETE /v1/control/partners/{id}", a.ctlDrop(a.dropPartnerRows))
	mux.HandleFunc("GET /v1/control/impact", a.ctlImpact)
	mux.HandleFunc("GET /v1/control/shares", a.ctlShares)
	mux.HandleFunc("GET /v1/control/webhooks/deliveries", a.ctlDeliveries)
	mux.HandleFunc("POST /v1/control/webhooks/deliveries/{id}/replay", a.ctlReplay)
	mux.HandleFunc("POST /v1/control/sync", a.ctlSync)
	mux.HandleFunc("POST /v1/control/reload", a.ctlReload)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		web.WriteError(w, r, http.StatusNotFound, "not_found", "Connect does not serve this control call.")
	})
	return web.WithRequestID(a.guard(mux))
}

func (a *App) guard(next http.Handler) http.Handler {
	port := strconv.Itoa(a.cfg.Connect.ConnectControlPort)
	hosts := map[string]bool{"127.0.0.1:" + port: true, "localhost:" + port: true, "[::1]:" + port: true}
	if a.link.Multi() {
		hosts[a.cfg.Connect.Host+":"+port] = true
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
		if r.Header.Get("Origin") != "" {
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

func (a *App) ctlHealth(w http.ResponseWriter, r *http.Request) {
	web.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "participant_id": kind, "world_id": a.WorldID()})
}

func (a *App) ctlStatus(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := control.ConnectState{Status: "ok", WorldID: a.st.WorldID, Registered: a.regReady, Connections: len(a.st.Connections), Deliveries: len(a.st.Deliveries), Warnings: append([]string{}, a.warnings...)}
	for _, c := range a.st.Connections {
		if c.Status == "active" || c.Status == "syncing" {
			out.Active++
		}
	}
	web.WriteJSON(w, http.StatusOK, out)
}

func pageParams(w http.ResponseWriter, r *http.Request) (int, string, bool) {
	limit := pageDefault
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > pageMax {
			web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "limit must be a number from 1 to "+strconv.Itoa(pageMax)+".")
			return 0, "", false
		}
		limit = n
	}
	return limit, r.URL.Query().Get("cursor"), true
}

func badCursor(w http.ResponseWriter, r *http.Request) {
	web.WriteError(w, r, http.StatusBadRequest, "invalid_cursor", "That cursor is not valid. Use the next_cursor value from the last page.")
}

func (a *App) connectionView(c *Connection) control.ConnectionView {
	var eff *string
	if c.EffectiveStart != nil {
		s := c.EffectiveStart.String()
		eff = &s
	}
	var exp *string
	if !c.ExpiresAt.IsZero() {
		s := ts(c.ExpiresAt)
		exp = &s
	}
	in := a.institutionJSON(c.Institution)
	return control.ConnectionView{
		ID:                 c.ID,
		Status:             c.Status,
		Institution:        control.InstitutionRef{ID: in["id"], Name: in["name"]},
		UserID:             c.UserID,
		StartDate:          c.StartDate.String(),
		EffectiveStartDate: eff,
		ConsentExpiresAt:   exp,
		LastSyncedAt:       tsp(c.LastSyncedAt),
		CreatedAt:          ts(c.CreatedAt),
		PartnerID:          c.PartnerID,
		CustomerID:         c.CustomerID,
		AccountIDs:         append([]string{}, c.AccountIDs...),
	}
}

func (a *App) ctlConnections(w http.ResponseWriter, r *http.Request) {
	limit, cursor, ok := pageParams(w, r)
	if !ok {
		return
	}
	bank, partner := r.URL.Query().Get("bank"), r.URL.Query().Get("partner")
	a.mu.Lock()
	rows := []*Connection{}
	for _, c := range a.st.Connections {
		if (bank == "" || c.Institution == bank) && (partner == "" || c.PartnerID == partner) {
			rows = append(rows, c)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			return rows[i].CreatedAt.Before(rows[j].CreatedAt)
		}
		return rows[i].ID < rows[j].ID
	})
	from := 0
	if cursor != "" {
		i := slices.IndexFunc(rows, func(c *Connection) bool { return c.ID == cursor })
		if i < 0 {
			a.mu.Unlock()
			badCursor(w, r)
			return
		}
		from = i + 1
	}
	out := []control.ConnectionView{}
	next := ""
	for i := from; i < len(rows); i++ {
		if len(out) == limit {
			next = out[len(out)-1].ID
			break
		}
		out = append(out, a.connectionView(rows[i]))
	}
	a.mu.Unlock()
	web.WriteJSON(w, http.StatusOK, web.ListPage(out, next))
}

func (a *App) ctlRevoke(w http.ResponseWriter, r *http.Request) {
	err := a.RevokeAtBank(r.Context(), r.PathValue("id"))
	if errors.Is(err, errNotFound) {
		web.WriteError(w, r, http.StatusNotFound, "not_found", "No such connection.")
		return
	}
	if err != nil {
		a.log.ErrorContext(r.Context(), "revoke consent", "connection", r.PathValue("id"), "err", err)
		web.WriteError(w, r, http.StatusBadGateway, "bank_unavailable", "The bank did not revoke the consent. Try again.")
		return
	}
	a.poke()
	web.WriteJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

func (a *App) ctlRevokeScope(revoke func(context.Context, string) (int, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		left, err := revoke(r.Context(), r.PathValue("id"))
		if err != nil {
			a.log.ErrorContext(r.Context(), "revoke connections", "id", r.PathValue("id"), "err", err)
			web.WriteError(w, r, http.StatusBadGateway, "revoke_failed", err.Error())
			return
		}
		a.poke()
		web.WriteJSON(w, http.StatusOK, control.ConnectRevoked{Remaining: left})
	}
}

func (a *App) ctlDrop(drop func(string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		drop(r.PathValue("id"))
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *App) ctlImpact(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	bank, partner := q.Get("bank"), q.Get("partner")
	if (bank == "") == (partner == "") {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "Send bank or partner, not both.")
		return
	}
	web.WriteJSON(w, http.StatusOK, a.impact(bank, partner))
}

func (a *App) impact(bank, partner string) control.ConnectImpact {
	a.mu.Lock()
	defer a.mu.Unlock()
	in := func(c *Connection) bool {
		return (bank != "" && c.Institution == bank) || (partner != "" && c.PartnerID == partner)
	}
	out := control.ConnectImpact{Open: []control.ConnectImpactConnection{}, Connected: []string{}}
	for _, c := range a.st.Connections {
		if !in(c) {
			continue
		}
		out.Total++
		if c.Status != "revoked" && c.Status != "expired" {
			out.Open = append(out.Open, control.ConnectImpactConnection{ID: c.ID, Partner: c.PartnerID, Customer: c.CustomerID, Status: c.Status})
		}
	}
	for _, rec := range a.st.Accounts {
		if c := a.st.Connections[rec.ConnectionID]; c != nil && in(c) && c.Status != "revoked" && c.Status != "expired" {
			out.Connected = append(out.Connected, rec.Key)
		}
	}
	sort.Slice(out.Open, func(i, j int) bool { return out.Open[i].ID < out.Open[j].ID })
	sort.Strings(out.Connected)
	return out
}

func (a *App) ctlShares(w http.ResponseWriter, r *http.Request) {
	key, line := r.URL.Query().Get("account"), r.URL.Query().Get("line")
	if key == "" {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "Send the account key in account.")
		return
	}
	a.mu.Lock()
	out := []control.ConnectShare{}
	for _, rec := range a.st.Accounts {
		if rec.Key != key {
			continue
		}
		sh := control.ConnectShare{Partner: rec.PartnerID, AccountID: rec.ID, Status: "revoked", AsAt: rec.AsAt}
		if c, _ := a.grantFor(rec); c != nil {
			sh.Status = c.Status
		}
		if line != "" {
			sh.TxnID = txnID(rec.PartnerID, key, line, false)
			if f := a.st.Feeds[rec.ID]; f != nil {
				_, sh.Synced = f.Lines[sh.TxnID]
			}
		}
		out = append(out, sh)
	}
	a.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	web.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (a *App) ctlDeliveries(w http.ResponseWriter, r *http.Request) {
	limit, cursor, ok := pageParams(w, r)
	if !ok {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	from := len(a.st.Deliveries) - 1
	if cursor != "" {
		from = slices.IndexFunc(a.st.Deliveries, func(d *Delivery) bool { return d.ID == cursor }) - 1
		if from < -1 {
			badCursor(w, r)
			return
		}
	}
	out := []control.DeliveryView{}
	next := ""
	for i := from; i >= 0; i-- {
		d := a.st.Deliveries[i]
		if len(out) == limit {
			next = out[len(out)-1].ID
			break
		}
		out = append(out, control.DeliveryView{ID: d.ID, Type: d.Type, Status: d.Status, Attempts: d.Attempts, LastStatus: d.LastStatus, LastError: d.LastError, Body: d.Body, CreatedAt: d.CreatedReal})
	}
	web.WriteJSON(w, http.StatusOK, web.ListPage(out, next))
}

func (a *App) ctlReplay(w http.ResponseWriter, r *http.Request) {
	if err := a.Replay(r.PathValue("id")); err != nil {
		web.WriteError(w, r, http.StatusNotFound, "not_found", "No such delivery.")
		return
	}
	web.WriteJSON(w, http.StatusOK, map[string]string{"status": "pending"})
}

func (a *App) ctlSync(w http.ResponseWriter, r *http.Request) {
	a.SyncAll(r.Context())
	web.WriteJSON(w, http.StatusOK, map[string]string{"status": "synced"})
}

func (a *App) ctlReload(w http.ResponseWriter, r *http.Request) {
	if err := a.Reload(); err != nil {
		a.log.ErrorContext(r.Context(), "reload the setup", "err", err)
		web.WriteError(w, r, http.StatusInternalServerError, "reload_failed", "Connect could not read the setup. Try again.")
		return
	}
	web.WriteJSON(w, http.StatusOK, map[string]string{"status": "reloaded"})
}

func (a *App) SyncAll(ctx context.Context) {
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

func (a *App) dropBankRows(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for cid, c := range a.st.Connections {
		if c.Institution == id {
			a.vaultDrop(cid)
		}
	}
	for lid, l := range a.st.LinkSessions {
		if l.Institution == id {
			delete(a.st.LinkSessions, lid)
		}
	}
	a.st.Revokes = slices.DeleteFunc(a.st.Revokes, func(p PendingRevoke) bool { return p.Bank == id })
	a.dirty()
}

func (a *App) dropPartnerRows(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for cid, c := range a.st.Connections {
		if c.PartnerID == id {
			a.vaultDrop(cid)
			delete(a.st.Connections, cid)
		}
	}
	for aid, acct := range a.st.Accounts {
		if acct.PartnerID == id {
			delete(a.st.Accounts, aid)
			delete(a.st.Feeds, aid)
		}
	}
	for rid, ref := range a.st.Refreshes {
		if a.st.Accounts[ref.AccountID] == nil {
			delete(a.st.Refreshes, rid)
		}
	}
	for lid, l := range a.st.LinkSessions {
		if l.PartnerID == id {
			delete(a.st.LinkSessions, lid)
		}
	}
	a.st.Deliveries = slices.DeleteFunc(a.st.Deliveries, func(d *Delivery) bool { return d.PartnerID == id })
	a.dirty()
}
