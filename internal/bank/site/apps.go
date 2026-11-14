package site

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/view"
)

type appRow struct {
	ID, Partner, Status string
	Since, Until        time.Time
	Accounts            []string
	LastAccess          time.Time
}

type accessRow struct {
	Partner, Account string
	At               time.Time
}

const (
	historyPageSize = 25
	maxHistoryPages = 1000
)

func (s *site) apps(w http.ResponseWriter, r *http.Request) {
	sess := s.user(w, r)
	if sess == nil {
		return
	}
	page, ok := view.PageParam(w, r, "access history", maxHistoryPages)
	if !ok {
		return
	}
	need := page*historyPageSize + 1
	ctx := r.Context()
	consents, err := s.Open.Consents(ctx, sess.Customer)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	access, err := s.Open.AccessHistory(ctx, sess.Customer, need)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	_, accts, err := s.mine(ctx, sess.Customer)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	byNUBAN := map[string]string{}
	for _, acc := range accts {
		byNUBAN[acc.NUBAN] = strings.Title(acc.Type) + " · " + acc.NUBAN
	}
	rows := []appRow{}
	for _, c := range consents {
		var status string
		switch c.Status {
		case openbanking.Authorised:
			status = "active"
		case openbanking.Revoked:
			status = "revoked"
		case openbanking.Expired:
			status = "expired"
		case openbanking.Dormant:
			status = "dormant"
		case openbanking.ReconsentRequired:
			status = "renew"
		default:
			continue
		}
		row := appRow{ID: c.ID, Partner: c.PartnerName, Status: status, Since: c.AuthorisedAt, Until: c.ExpiresAt, LastAccess: c.LastUsedAt}
		if row.Partner == "" {
			row.Partner = c.ClientID
		}
		for _, n := range c.Accounts {
			row.Accounts = append(row.Accounts, byNUBAN[n])
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Since.After(rows[j].Since) })
	history := []accessRow{}
	names := map[string]string{}
	for _, c := range consents {
		names[c.ID] = c.PartnerName
		if c.PartnerName == "" {
			names[c.ID] = c.ClientID
		}
	}
	for _, e := range access {
		history = append(history, accessRow{Partner: names[e.ConsentID], Account: byNUBAN[e.Account], At: e.At})
	}
	sort.SliceStable(history, func(i, j int) bool { return history[i].At.After(history[j].At) })
	if len(history) < need {
		if _, ok := view.PageParam(w, r, "access history", view.PagesOf(len(history), historyPageSize)); !ok {
			return
		}
	}
	start := min((page-1)*historyPageSize, len(history))
	end := min(page*historyPageSize, len(history))
	older := len(history) > end
	history = history[start:end]
	s.render(w, "bank_apps", http.StatusOK, sess, data{"Title": "Connected apps", "Nav": "apps", "Apps": rows, "History": history, "Page": page, "Older": older, "Newer": page > 1, "Revoked": r.URL.Query().Get("revoked") == "1", "Via": s.Brand})
}
