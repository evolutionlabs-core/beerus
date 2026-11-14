package site

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/view"
)

const statementPage = 30

type account struct {
	ledger.Account
	Type      string
	Ledger    int64
	Available int64
}

type line struct {
	bank.FeedLine
	Description string
	Href        string
}

func newLine(l bank.FeedLine, href bool) line {
	v := line{FeedLine: l, Description: bank.Normalise(l.Narration)}
	if href {
		v.Href = "/accounts/" + l.NUBAN + "/tx/" + url.PathEscape(l.Key)
	}
	return v
}

func (s *site) mine(ctx context.Context, customer string) (*bank.Bank, []account, error) {
	books, err := s.Host.Books()
	if err != nil {
		return nil, nil, err
	}
	accts, err := books.CustomerAccounts(ctx, customer)
	if err != nil {
		return nil, nil, err
	}
	out := []account{}
	for _, acc := range accts {
		bal, err := books.L.Balance(ctx, acc.Code)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, account{Account: acc, Type: acc.Product, Ledger: bal, Available: bal})
	}
	return books, out, nil
}

func (s *site) one(ctx context.Context, customer, nuban string) (*bank.Bank, account, bool, error) {
	books, accts, err := s.mine(ctx, customer)
	if err != nil {
		return nil, account{}, false, err
	}
	for _, v := range accts {
		if v.NUBAN == nuban {
			return books, v, true, nil
		}
	}
	return books, account{}, false, nil
}

func (s *site) accounts(w http.ResponseWriter, r *http.Request) {
	sess := s.user(w, r)
	if sess == nil {
		return
	}
	_, accts, err := s.mine(r.Context(), sess.Customer)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var total int64
	for _, v := range accts {
		total += v.Available
	}
	s.render(w, "bank_accounts", http.StatusOK, sess, data{"Title": "Accounts", "Nav": "accounts", "Accounts": accts, "Total": total})
}

func (s *site) statement(w http.ResponseWriter, r *http.Request) {
	sess := s.user(w, r)
	if sess == nil {
		return
	}
	ctx := r.Context()
	books, acc, ok, err := s.one(ctx, sess.Customer, r.PathValue("nuban"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	cursor := r.URL.Query().Get("cursor")
	ls, next, err := books.Recent(ctx, acc.NUBAN, statementPage, cursor)
	if errors.Is(err, bank.ErrBadCursor) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	lines := []line{}
	for _, l := range ls {
		lines = append(lines, newLine(l, true))
	}
	s.render(w, "bank_statement", http.StatusOK, sess, data{"Title": acc.Name, "Nav": "accounts", "A": acc, "Lines": lines, "Next": next, "First": cursor != "", "Transfer": view.TransferOutcome(r.URL.Query().Get("transfer"))})
}

func (s *site) tx(w http.ResponseWriter, r *http.Request) {
	sess := s.user(w, r)
	if sess == nil {
		return
	}
	ctx := r.Context()
	books, acc, ok, err := s.one(ctx, sess.Customer, r.PathValue("nuban"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	l, err := books.Line(ctx, acc.NUBAN, r.PathValue("key"))
	if errors.Is(err, bank.ErrUnknownLine) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	related := []line{}
	if l.SessionID != "" {
		ls, err := books.SessionLines(ctx, l.SessionID)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		for _, x := range ls {
			if x.NUBAN == acc.NUBAN && x.Key != l.Key {
				related = append(related, newLine(x, true))
			}
		}
	}
	s.render(w, "bank_tx", http.StatusOK, sess, data{"Title": bank.Normalise(l.Narration), "Nav": "accounts", "A": acc, "L": newLine(l, false), "Related": related})
}
