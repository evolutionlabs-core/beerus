package openbanking

import (
	"cmp"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

type cursor struct {
	date clock.Date
	seq  int64
}

func cursorOf(t Txn) cursor {
	return cursor{t.ValueDate, t.Seq}
}

func (c cursor) compare(o cursor) int {
	switch {
	case c.date.Before(o.date):
		return -1
	case o.date.Before(c.date):
		return 1
	}
	return cmp.Compare(c.seq, o.seq)
}

func (c cursor) String() string {
	return c.date.String() + "." + strconv.FormatInt(c.seq, 10)
}

func parseCursor(v string) (cursor, error) {
	day, seq, ok := strings.Cut(v, ".")
	if !ok {
		return cursor{}, errors.New("cursor is not valid")
	}
	d, err := clock.ParseDate(day)
	if err != nil {
		return cursor{}, err
	}
	n, err := strconv.ParseInt(seq, 10, 64)
	if err != nil {
		return cursor{}, err
	}
	return cursor{d, n}, nil
}

type granted struct {
	consent *Consent
	client  *Client
}

func (s *Server) resource(scope string, next func(http.ResponseWriter, *http.Request, granted)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		client, thumb, ok := s.caller(w, r)
		if !ok {
			return
		}
		raw := bearer(r)
		if raw == "" {
			fail(w, http.StatusUnauthorized, "invalid_token", "Send the access token as a Bearer token.")
			return
		}
		tok, err := s.cfg.Store.Token(r.Context(), Hash(raw))
		if err != nil || tok.Kind != "access" || tok.ClientID != client.ID {
			fail(w, http.StatusUnauthorized, "invalid_token", "The access token is not valid.")
			return
		}
		if tok.Thumbprint != thumb {
			fail(w, http.StatusUnauthorized, "invalid_token", "The access token is bound to another certificate.")
			return
		}
		c, err := s.live(r.Context(), tok.ConsentID)
		if err != nil {
			var ce consentError
			if errors.As(err, &ce) {
				fail(w, http.StatusForbidden, ce.code, "The customer's consent is not active.")
				return
			}
			fail(w, http.StatusInternalServerError, "server_error", "Something went wrong.")
			return
		}
		if tok.Revoked || s.cfg.Now().After(tok.ExpiresAt) {
			fail(w, http.StatusUnauthorized, "invalid_token", "The access token is not valid.")
			return
		}
		if !slices.Contains(c.Scopes, scope) {
			fail(w, http.StatusForbidden, "insufficient_scope", "The customer did not share this kind of data.")
			return
		}
		next(w, r, granted{consent: c, client: client})
	}
}

func (s *Server) touch(w http.ResponseWriter, r *http.Request, c *Consent, accounts ...string) bool {
	now := s.cfg.Biz()
	err := s.cfg.Store.MarkUsed(r.Context(), c.ID, now)
	for _, a := range accounts {
		if err == nil {
			err = s.cfg.Store.RecordAccess(r.Context(), Access{ConsentID: c.ID, Account: a, At: now})
		}
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "server_error", "Something went wrong.")
		return false
	}
	return true
}

func (s *Server) account(w http.ResponseWriter, r *http.Request, g granted) (AccountInfo, bool) {
	id := r.PathValue("id")
	if !slices.Contains(g.consent.Accounts, id) {
		fail(w, http.StatusForbidden, "account_not_consented", "The customer did not share this account.")
		return AccountInfo{}, false
	}
	info, err := s.cfg.Data.Account(id)
	if err != nil {
		fail(w, http.StatusNotFound, "account_not_found", "No such account.")
		return AccountInfo{}, false
	}
	return info, true
}

func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request, g granted) {
	out := []AccountInfo{}
	for _, id := range g.consent.Accounts {
		if info, err := s.cfg.Data.Account(id); err == nil {
			out = append(out, info)
		}
	}
	if !s.touch(w, r, g.consent) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"consent_id": g.consent.ID, "transaction_from": g.consent.TransactionFrom.String(), "consent_expires_at": g.consent.ExpiresAt.UTC().Format(time.RFC3339Nano), "data": out})
}

func (s *Server) getAccount(w http.ResponseWriter, r *http.Request, g granted) {
	info, ok := s.account(w, r, g)
	if !ok {
		return
	}
	if !s.touch(w, r, g.consent) {
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func money(minor int64, currency string) map[string]any {
	return map[string]any{"amount_minor": minor, "currency": currency}
}

func (s *Server) getBalances(w http.ResponseWriter, r *http.Request, g granted) {
	info, ok := s.account(w, r, g)
	if !ok {
		return
	}
	if v := r.URL.Query().Get("as_of"); v != "" {
		d, err := clock.ParseDate(v)
		today := clock.DateOf(s.cfg.Biz())
		if err != nil || d.After(today.AddDays(-1)) || d.Before(s.HistoryFloor().AddDays(-1)) {
			fail(w, http.StatusBadRequest, "invalid_request", "as_of must be a past date inside the bank's history, in YYYY-MM-DD form.")
			return
		}
		ledger, err := s.cfg.Data.BalanceOn(info.ID, d)
		if err != nil {
			fail(w, http.StatusInternalServerError, "server_error", "Something went wrong.")
			return
		}
		if !s.touch(w, r, g.consent, info.ID) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"account_id": info.ID, "as_of": d.String(), "ledger_balance": money(ledger, info.Currency)})
		return
	}
	b, err := s.cfg.Data.Balance(info.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "server_error", "Something went wrong.")
		return
	}
	if !s.touch(w, r, g.consent, info.ID) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"account_id":        info.ID,
		"as_of":             s.cfg.Biz().UTC().Format(time.RFC3339),
		"ledger_balance":    money(b.Ledger, b.Currency),
		"available_balance": money(b.Available, b.Currency),
	})
}

func txnJSON(t Txn, currency string) map[string]any {
	status := "booked"
	var booked any
	if t.Pending {
		status = "pending"
	} else {
		booked = t.BookedAt.UTC().Format(time.RFC3339)
	}
	var cp any
	if t.Counterparty != nil {
		cp = map[string]string{"name": t.Counterparty.Name, "account_number": t.Counterparty.AccountNumber, "bank_code": t.Counterparty.BankCode, "bank_name": t.Counterparty.BankName}
	}
	return map[string]any{
		"id":                  t.ID,
		"sequence":            t.Seq,
		"status":              status,
		"amount_minor":        t.Amount,
		"currency":            currency,
		"booked_at":           booked,
		"value_date":          t.ValueDate.String(),
		"description":         t.Narration,
		"counterparty":        cp,
		"session_id":          t.SessionID,
		"bank_reference":      t.BankRef,
		"channel":             t.Channel,
		"balance_after_minor": t.BalanceAfter,
		"had_pending":         t.HadPending,
	}
}

func (s *Server) getTransactions(w http.ResponseWriter, r *http.Request, g granted) {
	info, ok := s.account(w, r, g)
	if !ok {
		return
	}
	q := r.URL.Query()
	today := clock.DateOf(s.cfg.Biz())
	from, to := g.consent.TransactionFrom, today.AddDays(30)
	bad := func(msg string) {
		fail(w, http.StatusBadRequest, "invalid_request", msg)
	}
	if v := q.Get("from"); v != "" {
		d, err := clock.ParseDate(v)
		if err != nil {
			bad("from must be a date in YYYY-MM-DD form.")
			return
		}
		if d.After(from) {
			from = d
		}
	}
	if v := q.Get("to"); v != "" {
		d, err := clock.ParseDate(v)
		if err != nil {
			bad("to must be a date in YYYY-MM-DD form.")
			return
		}
		if d.Before(to) {
			to = d
		}
	}
	size := 100
	var after *cursor
	if v := q.Get("page_size"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			bad("page_size must be an integer from 1 to 500.")
			return
		}
		size = n
	}
	if v := q.Get("cursor"); v != "" {
		c, err := parseCursor(v)
		if err != nil {
			bad("cursor is not valid.")
			return
		}
		after = &c
	}
	rows, err := s.cfg.Data.Transactions(info.ID, from, to)
	if err != nil {
		fail(w, http.StatusInternalServerError, "server_error", "Something went wrong.")
		return
	}
	slices.SortStableFunc(rows, func(a, b Txn) int {
		return cursorOf(a).compare(cursorOf(b))
	})
	if after != nil {
		rows = rows[sort.Search(len(rows), func(i int) bool { return cursorOf(rows[i]).compare(*after) > 0 }):]
	}
	var next any
	if len(rows) > size {
		rows = rows[:size]
		next = cursorOf(rows[size-1]).String()
	}
	data := make([]map[string]any, 0, len(rows))
	for _, t := range rows {
		data = append(data, txnJSON(t, info.Currency))
	}
	if !s.touch(w, r, g.consent, info.ID) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account_id": info.ID, "from": from.String(), "data": data, "next_cursor": next})
}
