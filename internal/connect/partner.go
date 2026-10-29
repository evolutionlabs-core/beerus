package connect

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

const (
	linkTokenLife   = 30 * time.Minute
	linkSessionLife = 60 * time.Minute
	codeLife        = 10 * time.Minute
)

func hashSecret(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (a *App) PartnerHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/link_sessions", a.idempotent(a.createLinkSession))
	mux.HandleFunc("GET /v1/link_sessions/{id}", a.getLinkSession)
	mux.HandleFunc("GET /v1/webhook_keys", a.listWebhookKeys)
	mux.HandleFunc("POST /v1/connections/exchange", a.idempotent(a.exchange))
	mux.HandleFunc("GET /v1/connections/{id}", a.getConnection)
	mux.HandleFunc("DELETE /v1/connections/{id}", a.idempotent(a.deleteConnection))
	mux.HandleFunc("GET /v1/connections/{id}/accounts", a.listAccounts)
	mux.HandleFunc("POST /v1/connections/{id}/reauth", a.idempotent(a.reauth))
	mux.HandleFunc("GET /v1/accounts/{id}", a.getAccount)
	mux.HandleFunc("GET /v1/accounts/{id}/balance", a.getBalance)
	mux.HandleFunc("GET /v1/accounts/{id}/transactions/sync", a.syncTransactions)
	mux.HandleFunc("POST /v1/accounts/{id}/refresh", a.idempotent(a.refresh))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusNotFound, "not_found", "No such endpoint.")
	})
	return a.withRequestID(a.authenticate(mux))
}

func ts(t time.Time) string {
	return t.In(clock.Lagos).Format(time.RFC3339)
}

func tsp(t *time.Time) *string {
	if t == nil || t.IsZero() {
		return nil
	}
	s := ts(*t)
	return &s
}

func (a *App) linkSessionJSON(ls *LinkSession, token string) map[string]any {
	var conn any
	if ls.ConnectionID != "" {
		conn = ls.ConnectionID
	}
	m := map[string]any{
		"id":            ls.ID,
		"status":        ls.Status,
		"start_date":    ls.StartDate.String(),
		"workspace_id":  ls.WorkspaceID,
		"connection_id": conn,
		"created_at":    ts(ls.CreatedAt),
	}
	if token != "" {
		m["link_token"] = token
		m["link_token_expires_at"] = ts(ls.TokenExpires)
		m["widget_url"] = a.cfg.WidgetURL() + "/link?token=" + url.QueryEscape(token)
	}
	return m
}

func (a *App) newLinkSession(p *config.Partner, workspace string, start clock.Date, redirect, state, connectionID, institution string) (*LinkSession, string) {
	token := newSecret("lt_", 20)
	real := a.clock.Real()
	ls := &LinkSession{
		ID:           newID("lks"),
		PartnerID:    p.ID,
		WorkspaceID:  workspace,
		Status:       "created",
		StartDate:    start,
		RedirectURI:  redirect,
		State:        state,
		ConnectionID: connectionID,
		Institution:  institution,
		TokenHash:    hashSecret(token),
		TokenExpires: real.Add(linkTokenLife),
		CreatedAt:    a.clock.Now(),
		ExpiresReal:  real.Add(linkSessionLife),
	}
	a.st.LinkSessions[ls.ID] = ls
	a.dirty()
	return ls, token
}

func (a *App) createLinkSession(w http.ResponseWriter, r *http.Request) {
	p := partnerFrom(r)
	var body struct {
		StartDate   string `json:"start_date"`
		WorkspaceID string `json:"workspace_id"`
		RedirectURI string `json:"redirect_uri"`
		State       string `json:"state"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The body must be a JSON object with start_date, workspace_id and redirect_uri.")
		return
	}
	start, err := clock.ParseDate(body.StartDate)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "start_date must be a date in YYYY-MM-DD form.")
		return
	}
	if body.WorkspaceID == "" || len(body.WorkspaceID) > 128 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "workspace_id must be 1 to 128 characters.")
		return
	}
	if len(body.State) > 512 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "state must be at most 512 characters.")
		return
	}
	if !slices.Contains(p.RedirectURIs, body.RedirectURI) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "redirect_uri must exactly match a registered redirect URI.")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if start.After(a.clock.Today()) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "start_date must not be in the future.")
		return
	}
	ls, token := a.newLinkSession(p, body.WorkspaceID, start, body.RedirectURI, body.State, "", "")
	writeJSON(w, http.StatusCreated, a.linkSessionJSON(ls, token))
}

func (a *App) getLinkSession(w http.ResponseWriter, r *http.Request) {
	p := partnerFrom(r)
	a.mu.Lock()
	defer a.mu.Unlock()
	ls := a.st.LinkSessions[r.PathValue("id")]
	if ls == nil || ls.PartnerID != p.ID {
		writeError(w, r, http.StatusNotFound, "not_found", "No such link session.")
		return
	}
	a.expireSession(ls)
	writeJSON(w, http.StatusOK, a.linkSessionJSON(ls, ""))
}

func (a *App) expireSession(ls *LinkSession) {
	switch ls.Status {
	case "exchanged", "expired":
		return
	}
	if a.clock.Real().After(ls.ExpiresReal) {
		ls.Status = "expired"
		a.dirty()
	}
}

func (a *App) listWebhookKeys(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"data": []map[string]string{{"key_id": a.key.ID, "public_key": a.key.PublicWH(), "status": "active"}}})
}

func (a *App) effectiveStart(start clock.Date, institution string) (clock.Date, bool) {
	inst, _ := a.world.Institution(institution)
	today := a.clock.Today()
	min := today.AddDays(-inst.HistoryDays)
	genesis := a.world.Config().Genesis.AddDays(1)
	if genesis.After(min) {
		min = genesis
	}
	if start.Before(min) {
		return min, true
	}
	return start, false
}

func (a *App) exchange(w http.ResponseWriter, r *http.Request) {
	p := partnerFrom(r)
	var body struct {
		Code          string `json:"code"`
		LinkSessionID string `json:"link_session_id"`
	}
	if err := decodeBody(r, &body); err != nil || body.Code == "" || body.LinkSessionID == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The body must be a JSON object with code and link_session_id.")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	ls := a.st.LinkSessions[body.LinkSessionID]
	if ls == nil {
		writeError(w, r, http.StatusNotFound, "invalid_code", "The code or link session is unknown.")
		return
	}
	if ls.PartnerID != p.ID {
		writeError(w, r, http.StatusForbidden, "link_session_mismatch", "The link session belongs to another partner.")
		return
	}
	h := hashSecret(body.Code)
	if ls.CodeHash != h {
		for _, other := range a.st.LinkSessions {
			if other.CodeHash == h {
				writeError(w, r, http.StatusForbidden, "link_session_mismatch", "The code belongs to another link session.")
				return
			}
		}
		writeError(w, r, http.StatusNotFound, "invalid_code", "The code is unknown.")
		return
	}
	if ls.CodeUsed || ls.Status == "exchanged" {
		writeError(w, r, http.StatusNotFound, "invalid_code", "The code was already used.")
		return
	}
	if a.clock.Real().After(ls.CodeExpires) {
		writeError(w, r, http.StatusNotFound, "invalid_code", "The code has expired.")
		return
	}
	if ls.Status != "authorised" {
		writeError(w, r, http.StatusConflict, "link_session_expired", "The link session is not authorised.")
		return
	}
	ls.CodeUsed = true
	ls.Status = "exchanged"
	status := http.StatusCreated
	var c *Connection
	if ls.ConnectionID != "" {
		c = a.st.Connections[ls.ConnectionID]
		if c == nil {
			writeError(w, r, http.StatusNotFound, "not_found", "The connection no longer exists.")
			return
		}
		if c.Status == "revoked" {
			ls.Status = "expired"
			a.dirty()
			writeError(w, r, http.StatusConflict, "connection_revoked", "The connection was revoked. Create a new link session.")
			return
		}
		status = http.StatusOK
	} else {
		c = &Connection{ID: newID("con"), PartnerID: p.ID, WorkspaceID: ls.WorkspaceID, Institution: ls.Institution, PersonaID: ls.PersonaID, StartDate: ls.StartDate, CreatedAt: a.clock.Now()}
		a.st.Connections[c.ID] = c
		ls.ConnectionID = c.ID
	}
	a.authorise(c, ls)
	writeJSON(w, status, a.connectionJSON(c))
}

func (a *App) authorise(c *Connection, ls *LinkSession) {
	now := a.clock.Now()
	eff, _ := a.effectiveStart(c.StartDate, c.Institution)
	c.ConsentID = newID("cns")
	c.AuthorisedAt = now
	c.ExpiresAt = now.AddDate(1, 0, 0)
	c.PersonaID = ls.PersonaID
	old := map[string]bool{}
	for _, id := range c.AccountIDs {
		old[id] = true
	}
	inst, _ := a.world.Institution(c.Institution)
	next := []string{}
	var earliest *clock.Date
	for _, key := range ls.Accounts {
		wa, ok := a.world.Account(key)
		if !ok {
			continue
		}
		id := AccountID(c.PartnerID, inst.ID, wa.NUBAN)
		next = append(next, id)
		rec := a.st.Accounts[id]
		if rec == nil {
			rec = &AccountRec{ID: id, PartnerID: c.PartnerID, WorldKey: key, From: eff}
			a.st.Accounts[id] = rec
		} else if eff.Before(rec.From) {
			rec.From = eff
		}
		rec.Cut = rec.From != c.StartDate
		rec.HistoryDone = false
		rec.ConnectionID = c.ID
		if !slices.Contains(rec.Grants, c.ID) {
			rec.Grants = append(rec.Grants, c.ID)
		}
		rec.Removed = false
		if earliest == nil || rec.From.Before(*earliest) {
			f := rec.From
			earliest = &f
		}
		if !old[id] {
			a.emit(c.PartnerID, "account.added", map[string]any{"connection_id": c.ID, "account_id": id})
		}
		_, _ = a.fetchAccount(c.PartnerID, rec, now)
	}
	c.EffectiveStart = nil
	if earliest != nil && *earliest != c.StartDate {
		c.EffectiveStart = earliest
	}
	for id := range old {
		if slices.Contains(next, id) {
			continue
		}
		if rec := a.st.Accounts[id]; rec != nil {
			rec.Grants = slices.DeleteFunc(rec.Grants, func(g string) bool { return g == c.ID })
			rec.Removed = len(rec.Grants) == 0
			if rec.ConnectionID == c.ID && len(rec.Grants) > 0 {
				rec.ConnectionID = rec.Grants[len(rec.Grants)-1]
			}
		}
		a.emit(c.PartnerID, "account.removed", map[string]any{"connection_id": c.ID, "account_id": id})
	}
	c.AccountIDs = next
	c.SyncDueReal = a.clock.Real().Add(time.Second)
	if c.Status == "" {
		c.Status = "syncing"
	} else {
		a.setStatus(c, "syncing", "")
	}
	a.dirty()
	a.poke()
}

func (a *App) connectionJSON(c *Connection) map[string]any {
	inst, _ := a.world.Institution(c.Institution)
	var eff any
	if c.EffectiveStart != nil {
		eff = c.EffectiveStart.String()
	}
	var exp any
	if !c.ExpiresAt.IsZero() {
		exp = ts(c.ExpiresAt)
	}
	return map[string]any{
		"id":                   c.ID,
		"status":               c.Status,
		"institution":          map[string]string{"id": inst.ID, "name": inst.Name},
		"workspace_id":         c.WorkspaceID,
		"start_date":           c.StartDate.String(),
		"effective_start_date": eff,
		"consent_expires_at":   exp,
		"last_synced_at":       tsp(c.LastSyncedAt),
		"created_at":           ts(c.CreatedAt),
	}
}

func (a *App) connectionFor(w http.ResponseWriter, r *http.Request) *Connection {
	p := partnerFrom(r)
	c := a.st.Connections[r.PathValue("id")]
	if c == nil || c.PartnerID != p.ID {
		writeError(w, r, http.StatusNotFound, "not_found", "No such connection.")
		return nil
	}
	return c
}

func (a *App) getConnection(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if c := a.connectionFor(w, r); c != nil {
		writeJSON(w, http.StatusOK, a.connectionJSON(c))
	}
}

func (a *App) deleteConnection(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.connectionFor(w, r)
	if c == nil {
		return
	}
	if c.Status != "revoked" {
		if a.world.Down(c.Institution, a.clock.Now()) {
			w.Header().Set("Retry-After", "60")
			writeError(w, r, http.StatusServiceUnavailable, "institution_down", "The bank cannot be reached to revoke the consent.")
			return
		}
		a.setStatus(c, "revoked", "connection_revoked")
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) accountJSON(rec *AccountRec, connID string) map[string]any {
	wa, _ := a.world.Account(rec.WorldKey)
	inst, _ := a.world.Institution(wa.Institution)
	asAt := rec.AsAt
	if asAt.IsZero() {
		asAt = a.clock.Now()
	}
	return map[string]any{
		"id":             rec.ID,
		"connection_id":  connID,
		"institution":    map[string]string{"id": inst.ID, "name": inst.Name},
		"name":           wa.Name,
		"account_number": wa.NUBAN,
		"type":           wa.Type,
		"currency":       wa.Currency,
		"balances": map[string]any{
			"current":   map[string]any{"amount_minor": rec.Ledger, "currency": wa.Currency},
			"available": map[string]any{"amount_minor": rec.Available, "currency": wa.Currency},
			"as_at":     ts(asAt),
		},
	}
}

func (a *App) listAccounts(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.connectionFor(w, r)
	if c == nil {
		return
	}
	out := []map[string]any{}
	for _, id := range c.AccountIDs {
		if rec := a.st.Accounts[id]; rec != nil && !rec.Removed {
			out = append(out, a.accountJSON(rec, c.ID))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (a *App) reauth(w http.ResponseWriter, r *http.Request) {
	p := partnerFrom(r)
	var body struct {
		RedirectURI string `json:"redirect_uri"`
		State       string `json:"state"`
	}
	raw, _ := io.ReadAll(r.Body)
	if len(raw) > 0 {
		r.Body = io.NopCloser(bytes.NewReader(raw))
		if err := decodeBody(r, &body); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "The body must be a JSON object with optional redirect_uri and state.")
			return
		}
	}
	if body.RedirectURI == "" && len(p.RedirectURIs) > 0 {
		body.RedirectURI = p.RedirectURIs[0]
	}
	if !slices.Contains(p.RedirectURIs, body.RedirectURI) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "redirect_uri must exactly match a registered redirect URI.")
		return
	}
	if len(body.State) > 512 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "state must be at most 512 characters.")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.connectionFor(w, r)
	if c == nil {
		return
	}
	if c.Status == "revoked" {
		writeError(w, r, http.StatusConflict, "connection_revoked", "The connection was revoked. Create a new link session.")
		return
	}
	ls, token := a.newLinkSession(p, c.WorkspaceID, c.StartDate, body.RedirectURI, body.State, c.ID, c.Institution)
	writeJSON(w, http.StatusCreated, a.linkSessionJSON(ls, token))
}

func (a *App) grantFor(rec *AccountRec) (*Connection, string) {
	var best *Connection
	code := "connection_revoked"
	for i := len(rec.Grants) - 1; i >= 0; i-- {
		c := a.st.Connections[rec.Grants[i]]
		if c == nil {
			continue
		}
		switch c.Status {
		case "active", "syncing", "institution_down", "error":
			if best == nil {
				best = c
			}
		case "expired", "reauth_required":
			code = "reauth_required"
		}
	}
	return best, code
}

func (a *App) accountFor(w http.ResponseWriter, r *http.Request) (*AccountRec, *Connection) {
	p := partnerFrom(r)
	rec := a.st.Accounts[r.PathValue("id")]
	if rec == nil || rec.PartnerID != p.ID || rec.Removed {
		writeError(w, r, http.StatusNotFound, "not_found", "No such account.")
		return nil, nil
	}
	c, code := a.grantFor(rec)
	if c == nil {
		if code == "reauth_required" {
			writeError(w, r, http.StatusConflict, code, "The customer must consent again.")
		} else {
			writeError(w, r, http.StatusConflict, code, "The connection was revoked. Create a new link session.")
		}
		return nil, nil
	}
	return rec, c
}

func (a *App) getAccount(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if rec, c := a.accountFor(w, r); rec != nil {
		writeJSON(w, http.StatusOK, a.accountJSON(rec, c.ID))
	}
}

func (a *App) getBalance(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	rec, _ := a.accountFor(w, r)
	if rec == nil {
		return
	}
	d, err := clock.ParseDate(r.URL.Query().Get("as_of"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "as_of must be a date in YYYY-MM-DD form.")
		return
	}
	min := rec.From.AddDays(-1)
	max := a.clock.Today().AddDays(-1)
	if d.Before(min) || d.After(max) {
		writeError(w, r, http.StatusUnprocessableEntity, "as_of_out_of_range", "as_of must be between "+min.String()+" and "+max.String()+", the day before the first date Connect holds and the last completed business day.")
		return
	}
	bal, err := a.src(rec.WorldKey).EndOfDay(rec.WorldKey, d)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "internal_error", "Something went wrong.")
		return
	}
	wa, _ := a.world.Account(rec.WorldKey)
	var eff any
	if rec.Cut {
		eff = rec.From.String()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"account_id":           rec.ID,
		"as_of":                d.String(),
		"ledger_balance":       map[string]any{"amount_minor": bal, "currency": wa.Currency},
		"effective_start_date": eff,
	})
}

func (a *App) syncTransactions(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	rec, _ := a.accountFor(w, r)
	if rec == nil {
		return
	}
	limit := 100
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 500 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit must be an integer from 1 to 500.")
			return
		}
		limit = n
	}
	var from int64
	if s := r.URL.Query().Get("cursor"); s != "" {
		c, err := decodeCursor(s)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_cursor", "The cursor cannot be read.")
			return
		}
		if c.World != a.st.WorldID {
			writeError(w, r, http.StatusConflict, "cursor_reset", "The world was restored from a snapshot. Start again with no cursor.")
			return
		}
		if c.Account != rec.ID || c.Seq > a.st.Seq {
			writeError(w, r, http.StatusBadRequest, "invalid_cursor", "The cursor belongs to another account.")
			return
		}
		from = c.Seq
	}
	added := []Txn{}
	modified := []ModifiedTxn{}
	removed := []map[string]string{}
	last := from
	n := 0
	more := false
	if feed := a.st.Feeds[rec.ID]; feed != nil {
		for _, e := range feed.Log {
			if e.Seq <= from {
				continue
			}
			if n == limit {
				more = true
				break
			}
			switch e.Kind {
			case "added":
				added = append(added, e.Txn)
			case "modified":
				modified = append(modified, ModifiedTxn{Txn: e.Txn, ModificationReason: e.Reason})
			case "removed":
				removed = append(removed, map[string]string{"id": e.Txn.ID})
			}
			last = e.Seq
			n++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"added":       added,
		"modified":    modified,
		"removed":     removed,
		"next_cursor": encodeCursor(cursor{World: a.st.WorldID, Account: rec.ID, Seq: last}),
		"has_more":    more,
		"world_id":    a.st.WorldID,
	})
}

func (a *App) refresh(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	rec, _ := a.accountFor(w, r)
	if rec == nil {
		return
	}
	real := a.clock.Real()
	if wait := a.cfg.RefreshMinInterval() - real.Sub(rec.LastRefresh); !rec.LastRefresh.IsZero() && wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, r, http.StatusTooManyRequests, "rate_limited", "Too many refresh requests for this account.")
		return
	}
	rec.LastRefresh = real
	rf := &Refresh{ID: newID("ref"), AccountID: rec.ID, Status: "queued", RequestedAt: a.clock.Now()}
	a.st.Refreshes[rf.ID] = rf
	a.dirty()
	a.poke()
	writeJSON(w, http.StatusAccepted, map[string]any{"id": rf.ID, "account_id": rf.AccountID, "status": "queued", "requested_at": ts(rf.RequestedAt)})
}
