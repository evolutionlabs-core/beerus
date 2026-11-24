package aggregator

import (
	"bytes"
	"crypto/subtle"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

const (
	publicTokenPrefix = "public-"
	accessTokenPrefix = "access-"
	linkTokenLife     = 30 * time.Minute
	linkSessionLife   = 60 * time.Minute
	publicTokenLife   = 10 * time.Minute
)

func (a *App) PartnerHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/link/token/create", a.registered(a.idempotent(a.createLinkToken)))
	mux.HandleFunc("GET /v1/link_sessions/{id}", a.getLinkSession)
	mux.HandleFunc("GET /v1/webhook_keys", a.listWebhookKeys)
	mux.HandleFunc("GET /v1/institutions", a.listInstitutions)
	mux.HandleFunc("POST /v1/public_token/exchange", a.idempotent(a.exchange))
	mux.HandleFunc("GET /v1/connections/{id}", a.getConnection)
	mux.HandleFunc("DELETE /v1/connections/{id}", a.idempotent(a.deleteConnection))
	mux.HandleFunc("GET /v1/connections/{id}/accounts", a.listAccounts)
	mux.HandleFunc("POST /v1/connections/{id}/reauth", a.idempotent(a.reauth))
	mux.HandleFunc("GET /v1/accounts/{id}", a.getAccount)
	mux.HandleFunc("GET /v1/accounts/{id}/balance", a.getBalance)
	mux.HandleFunc("GET /v1/accounts/{id}/transactions/sync", a.syncTransactions)
	mux.HandleFunc("POST /v1/accounts/{id}/refresh", a.idempotent(a.refresh))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		web.WriteError(w, r, http.StatusNotFound, "not_found", "No such endpoint.")
	})
	return web.WithRequestID(a.authenticate(mux))
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
		"user_id":       ls.UserID,
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

func (a *App) newLinkSession(p *config.Partner, user string, start clock.Date, redirect, state, connectionID, institution string) (*LinkSession, string, error) {
	token, err := web.NewSecret("lt_", 20)
	if err != nil {
		return nil, "", err
	}
	real := a.clock.Real()
	ls := &LinkSession{
		ID:           web.NewID("lks"),
		PartnerID:    p.ID,
		UserID:       user,
		Status:       "created",
		StartDate:    start,
		RedirectURI:  redirect,
		State:        state,
		ConnectionID: connectionID,
		Institution:  institution,
		Fixed:        institution != "",
		TokenHash:    web.HashSecret(token),
		TokenExpires: real.Add(linkTokenLife),
		CreatedAt:    a.clock.Now(),
		ExpiresReal:  real.Add(linkSessionLife),
	}
	a.st.LinkSessions[ls.ID] = ls
	a.dirty()
	return ls, token, nil
}

func (a *App) createLinkToken(w http.ResponseWriter, r *http.Request) {
	p := partnerFrom(r)
	var body struct {
		UserID        string `json:"user_id"`
		InstitutionID string `json:"institution_id"`
		RedirectURI   string `json:"redirect_uri"`
		State         string `json:"state"`
	}
	if err := web.DecodeBody(r, &body); err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "The body must be a JSON object with user_id, an optional institution_id and redirect_uri.")
		return
	}
	if body.UserID == "" {
		web.WriteError(w, r, http.StatusUnprocessableEntity, "invalid_request", "user_id is required.")
		return
	}
	if len(body.UserID) > 128 {
		web.WriteError(w, r, http.StatusUnprocessableEntity, "invalid_request", "user_id must be at most 128 characters.")
		return
	}
	if len(body.State) > 512 {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "state must be at most 512 characters.")
		return
	}
	if !slices.Contains(p.RedirectURIs, body.RedirectURI) {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "redirect_uri must exactly match a registered redirect URI.")
		return
	}
	if body.InstitutionID != "" {
		if b := a.bank(body.InstitutionID); b == nil || b.Closed() {
			web.WriteError(w, r, http.StatusUnprocessableEntity, "invalid_request", "There is no bank "+body.InstitutionID+" in this setup. Use an institution_id from GET /v1/institutions.")
			return
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	start := a.clock.Today().AddDays(-a.linkDays)
	ls, token, err := a.newLinkSession(p, body.UserID, start, body.RedirectURI, body.State, "", body.InstitutionID)
	if err != nil {
		a.log.Error("link session", "err", err)
		web.WriteError(w, r, http.StatusInternalServerError, "internal_error", "The link token could not be created. Try again.")
		return
	}
	web.WriteJSON(w, http.StatusCreated, map[string]any{"link_token": token, "expiration": ts(ls.TokenExpires)})
}

func (a *App) getLinkSession(w http.ResponseWriter, r *http.Request) {
	p := partnerFrom(r)
	a.mu.Lock()
	defer a.mu.Unlock()
	ls := a.st.LinkSessions[r.PathValue("id")]
	if ls == nil || ls.PartnerID != p.ID {
		web.WriteError(w, r, http.StatusNotFound, "not_found", "No such link session.")
		return
	}
	a.expireSession(ls)
	web.WriteJSON(w, http.StatusOK, a.linkSessionJSON(ls, ""))
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
	web.WriteJSON(w, http.StatusOK, map[string]any{"data": []map[string]string{{"key_id": a.key.ID, "public_key": a.key.PublicWH(), "status": "active"}}})
}

func (a *App) listInstitutions(w http.ResponseWriter, r *http.Request) {
	limit, cursor, ok := pageParams(w, r)
	if !ok {
		return
	}
	banks := a.openInstitutions()
	sort.Slice(banks, func(i, j int) bool { return banks[i].ID < banks[j].ID })
	from := 0
	if cursor != "" {
		i := slices.IndexFunc(banks, func(b *config.Bank) bool { return b.ID == cursor })
		if i < 0 {
			badCursor(w, r)
			return
		}
		from = i + 1
	}
	out := []map[string]string{}
	next := ""
	for _, b := range banks[from:] {
		if len(out) == limit {
			next = out[len(out)-1]["institution_id"]
			break
		}
		out = append(out, map[string]string{"institution_id": b.ID, "name": b.Name})
	}
	body := map[string]any{"institutions": out}
	if next != "" {
		body["next_cursor"] = next
	}
	web.WriteJSON(w, http.StatusOK, body)
}

func historyStart(start, today clock.Date, days int) (clock.Date, bool) {
	if floor := today.AddDays(-days); start.Before(floor) {
		return floor, true
	}
	return start, false
}

func (a *App) effectiveStart(start clock.Date, institution string) (clock.Date, bool) {
	b := a.bank(institution)
	if b == nil {
		return start, false
	}
	return historyStart(start, a.clock.Today(), b.HistoryDays)
}

func (a *App) exchange(w http.ResponseWriter, r *http.Request) {
	p := partnerFrom(r)
	var body struct {
		PublicToken string `json:"public_token"`
	}
	if err := web.DecodeBody(r, &body); err != nil || body.PublicToken == "" {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "The body must be a JSON object with public_token.")
		return
	}
	access, err := web.NewSecret(accessTokenPrefix, 32)
	if err != nil {
		a.log.Error("access token", "err", err)
		web.WriteError(w, r, http.StatusInternalServerError, "internal_error", "The access token could not be made.")
		return
	}
	a.mu.Lock()
	ex, ok := a.beginExchangeLocked(w, r, p, body.PublicToken, web.HashSecret(access))
	a.mu.Unlock()
	if !ok {
		return
	}
	for _, rec := range ex.auth.accounts {
		a.mu.Lock()
		a.announceAccountLocked(ex.conn, ex.auth, rec)
		a.mu.Unlock()
		a.firstSync(ex.conn, rec, ex.auth.at)
	}
	a.mu.Lock()
	a.finishAuthoriseLocked(ex.conn, ex.auth)
	b := a.bank(ex.conn.Institution)
	a.mu.Unlock()
	if b != nil && ex.oldConsent != "" && ex.oldConsent != ex.consent {
		a.revokeOrQueue(b, ex.oldConsent)
	}
	w.Header().Set(web.SecretReplyHeader, "This public_token was already used.")
	web.WriteJSON(w, http.StatusOK, map[string]string{"access_token": access, "connection_id": ex.conn.ID})
}

type exchangeGrant struct {
	conn       *Connection
	oldConsent string
	consent    string
	auth       authorisation
}

func (a *App) announceAccountLocked(c *Connection, auth authorisation, rec *AccountRec) {
	if !auth.old[rec.ID] {
		a.emit(c.PartnerID, "account.added", map[string]any{"connection_id": c.ID, "account_id": rec.ID})
	}
}

func (a *App) firstSync(c *Connection, rec *AccountRec, at time.Time) {
	_, err := a.fetchAccount(c, rec, at)
	if err == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.st.Connections[c.ID] == c && a.bankFailedLocked(c, err) {
		return
	}
	a.log.Warn("first sync after linking failed", "connection", c.ID, "account", rec.ID, "err", err)
}

func (a *App) linkByPublicToken(token string) *LinkSession {
	h := web.HashSecret(token)
	for _, s := range a.st.LinkSessions {
		if s.PublicHash != "" && subtle.ConstantTimeCompare([]byte(s.PublicHash), []byte(h)) == 1 {
			return s
		}
	}
	return nil
}

func (a *App) beginExchangeLocked(w http.ResponseWriter, r *http.Request, p *config.Partner, token, accessHash string) (exchangeGrant, bool) {
	ls := a.linkByPublicToken(token)
	if ls == nil || ls.PartnerID != p.ID {
		web.WriteError(w, r, http.StatusNotFound, "invalid_public_token", "This public_token is not known.")
		return exchangeGrant{}, false
	}
	if ls.PublicUsed || ls.Status == "exchanged" {
		web.WriteError(w, r, http.StatusNotFound, "invalid_public_token", "This public_token was already used.")
		return exchangeGrant{}, false
	}
	if a.clock.Real().After(ls.PublicExpires) {
		web.WriteError(w, r, http.StatusNotFound, "invalid_public_token", "This public_token has expired. Start a new link.")
		return exchangeGrant{}, false
	}
	if ls.Status != "authorised" {
		web.WriteError(w, r, http.StatusConflict, "link_session_expired", "The link session is not authorised.")
		return exchangeGrant{}, false
	}
	ls.PublicUsed = true
	ls.Status = "exchanged"
	var c *Connection
	if ls.ConnectionID != "" {
		c = a.st.Connections[ls.ConnectionID]
		if c == nil {
			web.WriteError(w, r, http.StatusNotFound, "not_found", "The connection no longer exists.")
			return exchangeGrant{}, false
		}
		if c.Status == "revoked" {
			ls.Status = "expired"
			a.dirty()
			web.WriteError(w, r, http.StatusConflict, "connection_revoked", "The connection was revoked. Create a new link session.")
			return exchangeGrant{}, false
		}
	} else {
		c = &Connection{ID: web.NewID("con"), PartnerID: p.ID, UserID: ls.UserID, Institution: ls.Institution, CustomerID: ls.CustomerID, StartDate: ls.StartDate, CreatedAt: a.clock.Now()}
		a.st.Connections[c.ID] = c
		ls.ConnectionID = c.ID
	}
	oldConsent := c.ConsentID
	if err := a.vaultMove(linkVaultPrefix+ls.ID, c.ID); err != nil {
		a.dirty()
		web.WriteError(w, r, http.StatusInternalServerError, "internal_error", "The bank tokens could not be stored.")
		return exchangeGrant{}, false
	}
	c.AccessHash = accessHash
	auth := a.authoriseLocked(c, ls)
	return exchangeGrant{conn: c, oldConsent: oldConsent, consent: c.ConsentID, auth: auth}, true
}

type authorisation struct {
	at       time.Time
	accounts []*AccountRec
	old      map[string]bool
	next     []string
	earliest *clock.Date
}

func (a *App) authoriseLocked(c *Connection, ls *LinkSession) authorisation {
	now := a.clock.Now()
	eff, _ := a.effectiveStart(c.StartDate, c.Institution)
	c.ConsentID = web.NewID("cns")
	c.AuthorisedAt = now
	c.ExpiresAt = now.AddDate(1, 0, 0)
	if ls.BankConsentID != "" {
		c.ConsentID, c.ExpiresAt = ls.BankConsentID, ls.ConsentEnds
		if !ls.BankFrom.IsZero() && ls.BankFrom.After(eff) {
			eff = ls.BankFrom
		}
	}
	c.CustomerID = ls.CustomerID
	old := map[string]bool{}
	for _, id := range c.AccountIDs {
		old[id] = true
	}
	auth := authorisation{at: now, old: old, next: []string{}}
	for _, key := range ls.Accounts {
		bankID, nuban := splitKey(key)
		id := AccountID(c.PartnerID, bankID, nuban)
		auth.next = append(auth.next, id)
		rec := a.st.Accounts[id]
		if rec == nil {
			rec = &AccountRec{ID: id, PartnerID: c.PartnerID, Key: key, From: eff}
			a.st.Accounts[id] = rec
		} else {
			if eff.Before(rec.From) {
				rec.From = eff
			}
		}
		rec.NUBAN = nuban
		for _, ba := range ls.BankAccounts {
			if ba.ID == nuban {
				rec.Name, rec.Type, rec.Currency = ba.Name, ba.Type, ba.Currency
			}
		}
		rec.Cut = rec.From != c.StartDate
		rec.HistoryDone = false
		rec.ConnectionID = c.ID
		if !slices.Contains(rec.Grants, c.ID) {
			rec.Grants = append(rec.Grants, c.ID)
		}
		rec.Removed = false
		if auth.earliest == nil || rec.From.Before(*auth.earliest) {
			f := rec.From
			auth.earliest = &f
		}
		auth.accounts = append(auth.accounts, rec)
	}
	return auth
}

func (a *App) finishAuthoriseLocked(c *Connection, auth authorisation) {
	c.EffectiveStart = nil
	if auth.earliest != nil && *auth.earliest != c.StartDate {
		c.EffectiveStart = auth.earliest
	}
	for id := range auth.old {
		if slices.Contains(auth.next, id) {
			continue
		}
		if rec := a.st.Accounts[id]; rec != nil {
			rec.Grants = slices.DeleteFunc(rec.Grants, func(g string) bool { return g == c.ID })
			rec.Removed = !a.liveGrant(rec)
			if rec.ConnectionID == c.ID && len(rec.Grants) > 0 {
				rec.ConnectionID = rec.Grants[len(rec.Grants)-1]
			}
		}
		a.emit(c.PartnerID, "account.removed", map[string]any{"connection_id": c.ID, "account_id": id})
	}
	c.AccountIDs = auth.next
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
		"institution":          a.institutionJSON(c.Institution),
		"user_id":              c.UserID,
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
		web.WriteError(w, r, http.StatusNotFound, "not_found", "No such connection.")
		return nil
	}
	return c
}

func (a *App) getConnection(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if c := a.connectionFor(w, r); c != nil {
		web.WriteJSON(w, http.StatusOK, a.connectionJSON(c))
	}
}

func (a *App) deleteConnection(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	c := a.connectionFor(w, r)
	if c == nil {
		a.mu.Unlock()
		return
	}
	revoked := c.Status == "revoked"
	a.mu.Unlock()
	if revoked {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := a.revokeConnectionAtBank(c.ID); err != nil {
		w.Header().Set("Retry-After", "60")
		if isDown(err) {
			web.WriteError(w, r, http.StatusServiceUnavailable, "institution_down", "The bank cannot be reached to revoke the consent.")
		} else {
			web.WriteError(w, r, http.StatusBadGateway, "bank_error", "The bank did not accept the revocation.")
		}
		return
	}
	a.mu.Lock()
	if a.st.Connections[c.ID] == c {
		a.setStatus(c, "revoked", "connection_revoked")
	}
	a.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) institutionJSON(id string) map[string]string {
	name := ""
	if b := a.bank(id); b != nil {
		name = b.Name
	}
	return map[string]string{"id": id, "name": name}
}

func (a *App) accountJSON(rec *AccountRec, connID string) map[string]any {
	bankID, _ := splitKey(rec.Key)
	asAt := rec.AsAt
	if asAt.IsZero() {
		asAt = a.clock.Now()
	}
	return map[string]any{
		"id":             rec.ID,
		"connection_id":  connID,
		"institution":    a.institutionJSON(bankID),
		"name":           rec.Name,
		"account_number": rec.NUBAN,
		"type":           rec.Type,
		"currency":       rec.Currency,
		"balances": map[string]any{
			"current":   map[string]any{"amount_minor": rec.Ledger, "currency": rec.Currency},
			"available": map[string]any{"amount_minor": rec.Available, "currency": rec.Currency},
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
	if c.Status == "revoked" {
		web.WriteError(w, r, http.StatusConflict, "connection_revoked", "The connection was revoked. Create a new link session.")
		return
	}
	out := []map[string]any{}
	for _, id := range c.AccountIDs {
		if rec := a.st.Accounts[id]; rec != nil && !rec.Removed {
			out = append(out, a.accountJSON(rec, c.ID))
		}
	}
	web.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (a *App) reauth(w http.ResponseWriter, r *http.Request) {
	p := partnerFrom(r)
	var body struct {
		RedirectURI string `json:"redirect_uri"`
		State       string `json:"state"`
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "The request body could not be read.")
		return
	}
	if len(raw) > 0 {
		r.Body = io.NopCloser(bytes.NewReader(raw))
		if err := web.DecodeBody(r, &body); err != nil {
			web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "The body must be a JSON object with optional redirect_uri and state.")
			return
		}
	}
	if body.RedirectURI == "" && len(p.RedirectURIs) > 0 {
		body.RedirectURI = p.RedirectURIs[0]
	}
	if !slices.Contains(p.RedirectURIs, body.RedirectURI) {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "redirect_uri must exactly match a registered redirect URI.")
		return
	}
	if len(body.State) > 512 {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "state must be at most 512 characters.")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.connectionFor(w, r)
	if c == nil {
		return
	}
	if c.Status == "revoked" {
		web.WriteError(w, r, http.StatusConflict, "connection_revoked", "The connection was revoked. Create a new link session.")
		return
	}
	ls, token, err := a.newLinkSession(p, c.UserID, c.StartDate, body.RedirectURI, body.State, c.ID, c.Institution)
	if err != nil {
		a.log.Error("link session", "err", err)
		web.WriteError(w, r, http.StatusInternalServerError, "internal_error", "The link session could not be created. Try again.")
		return
	}
	web.WriteJSON(w, http.StatusCreated, a.linkSessionJSON(ls, token))
}

func (a *App) liveGrant(rec *AccountRec) bool {
	for _, id := range rec.Grants {
		if c := a.st.Connections[id]; c != nil && c.Status != "revoked" {
			return true
		}
	}
	return false
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
	if rec == nil || rec.PartnerID != p.ID {
		web.WriteError(w, r, http.StatusNotFound, "not_found", "No such account.")
		return nil, nil
	}
	if rec.Removed {
		web.WriteError(w, r, http.StatusConflict, "account_not_shared", "The customer no longer shares this account. Ask them to reconnect and share it.")
		return nil, nil
	}
	c, code := a.grantFor(rec)
	if c == nil {
		if code == "reauth_required" {
			web.WriteError(w, r, http.StatusConflict, code, "The customer must consent again.")
		} else {
			web.WriteError(w, r, http.StatusConflict, code, "The connection was revoked. Create a new link session.")
		}
		return nil, nil
	}
	return rec, c
}

func (a *App) getAccount(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if rec, c := a.accountFor(w, r); rec != nil {
		web.WriteJSON(w, http.StatusOK, a.accountJSON(rec, c.ID))
	}
}

type balanceQuery struct {
	rec  *AccountRec
	conn *Connection
	pull bankPull
	date clock.Date
}

func (a *App) balanceQueryLocked(w http.ResponseWriter, r *http.Request) (balanceQuery, bool) {
	rec, conn := a.accountFor(w, r)
	if rec == nil {
		return balanceQuery{}, false
	}
	d, err := clock.ParseDate(r.URL.Query().Get("as_of"))
	if err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "as_of must be a date in YYYY-MM-DD form.")
		return balanceQuery{}, false
	}
	min, max := rec.From.AddDays(-1), a.clock.Today().AddDays(-1)
	if d.Before(min) || d.After(max) {
		web.WriteError(w, r, http.StatusUnprocessableEntity, "as_of_out_of_range", "as_of must be between "+min.String()+" and "+max.String()+", the day before the first date Connect holds and the last completed business day.")
		return balanceQuery{}, false
	}
	pull, err := a.bankPullPlanLocked(conn, rec)
	if err != nil {
		a.balanceFailedLocked(w, r, conn, err)
		return balanceQuery{}, false
	}
	return balanceQuery{rec: rec, conn: conn, pull: pull, date: d}, true
}

func (a *App) balanceFailedLocked(w http.ResponseWriter, r *http.Request, conn *Connection, err error) {
	if a.st.Connections[conn.ID] == conn && a.bankFailedLocked(conn, err) {
		a.connUnavailable(w, r, conn)
		return
	}
	web.WriteError(w, r, http.StatusInternalServerError, "internal_error", "Something went wrong.")
}

func (a *App) getBalance(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	q, ok := a.balanceQueryLocked(w, r)
	a.mu.Unlock()
	if !ok {
		return
	}
	bal, err := a.bankBalanceOn(q.pull, q.date)
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.balanceFailedLocked(w, r, q.conn, err)
		return
	}
	var eff any
	if q.rec.Cut {
		eff = q.rec.From.String()
	}
	web.WriteJSON(w, http.StatusOK, map[string]any{
		"account_id":           q.rec.ID,
		"as_of":                q.date.String(),
		"ledger_balance":       map[string]any{"amount_minor": bal, "currency": q.rec.Currency},
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
			web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "limit must be an integer from 1 to 500.")
			return
		}
		limit = n
	}
	var from int64
	if s := r.URL.Query().Get("cursor"); s != "" {
		c, err := decodeCursor(s)
		if err != nil {
			web.WriteError(w, r, http.StatusBadRequest, "invalid_cursor", "The cursor cannot be read.")
			return
		}
		if c.World != a.st.WorldID {
			web.WriteError(w, r, http.StatusConflict, "cursor_reset", "The world was replaced after an unclean shutdown or a reset. Start again with no cursor.")
			return
		}
		if c.Account != rec.ID || c.Seq > a.st.Seq {
			web.WriteError(w, r, http.StatusBadRequest, "invalid_cursor", "The cursor belongs to another account.")
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
	web.WriteJSON(w, http.StatusOK, map[string]any{
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
		web.WriteError(w, r, http.StatusTooManyRequests, "rate_limited", "Too many refresh requests for this account.")
		return
	}
	rec.LastRefresh = real
	rf := &Refresh{ID: web.NewID("ref"), AccountID: rec.ID, Status: "queued", RequestedAt: a.clock.Now()}
	a.st.Refreshes[rf.ID] = rf
	a.dirty()
	a.poke()
	web.WriteJSON(w, http.StatusAccepted, map[string]any{"id": rf.ID, "account_id": rf.AccountID, "status": "queued", "requested_at": ts(rf.RequestedAt)})
}

func (a *App) connUnavailable(w http.ResponseWriter, r *http.Request, c *Connection) {
	switch c.Status {
	case "revoked":
		web.WriteError(w, r, http.StatusConflict, "connection_revoked", "The connection was revoked. Create a new link session.")
	case "institution_down":
		w.Header().Set("Retry-After", "60")
		web.WriteError(w, r, http.StatusServiceUnavailable, "institution_down", "The bank cannot be reached.")
	default:
		web.WriteError(w, r, http.StatusConflict, "reauth_required", "The customer must consent again.")
	}
}
