package server

import (
	"context"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"net/http"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

func entryOf(l bank.FeedLine) control.Entry {
	return control.Entry{ID: l.Key, Account: l.NUBAN, Seq: l.Seq, Amount: l.Amount, BalanceAfter: l.BalanceAfter, PostedAt: l.BookedAt.In(clock.Lagos), ValueDate: l.ValueDate.String(), Narration: l.Narration, Channel: l.Channel, SessionID: l.SessionID, Reference: l.BankRef, Counterparty: l.Counterparty}
}

func entriesOf(lines []bank.FeedLine) []control.Entry {
	out := make([]control.Entry, 0, len(lines))
	for _, l := range lines {
		out = append(out, entryOf(l))
	}
	return out
}

func (s *Banking) customerAccount(ctx context.Context, b *bank.Bank, nuban string) (ledger.Account, error) {
	acc, err := b.L.Account(ctx, bank.CustomerAccount(nuban))
	if err != nil {
		return ledger.Account{}, err
	}
	if !acc.Customer {
		return ledger.Account{}, ledger.ErrUnknownAccount
	}
	return acc, nil
}

func (s *Banking) Account(ctx context.Context, bankID, nuban string) (control.AccountJSON, error) {
	bk, err := s.bankBook(bankID)
	if err != nil {
		return control.AccountJSON{}, err
	}
	acc, err := s.customerAccount(ctx, bk.Bank, nuban)
	if err != nil {
		return control.AccountJSON{}, err
	}
	return accountJSON(ctx, bk.Bank, acc)
}

func (s *Banking) Totals(ctx context.Context, bankID string) (control.Totals, error) {
	bk, err := s.bankBook(bankID)
	if err != nil {
		return control.Totals{}, err
	}
	t, err := bk.Bank.Totals(ctx)
	if err != nil {
		return control.Totals{}, err
	}
	return control.Totals{Customers: t.Customers, Accounts: t.Accounts, Deposits: t.Deposits}, nil
}

func (s *Banking) Entries(ctx context.Context, bankID, nuban string, limit int, cursor string) ([]control.Entry, string, error) {
	bk, err := s.bankBook(bankID)
	if err != nil {
		return nil, "", err
	}
	if _, err := s.customerAccount(ctx, bk.Bank, nuban); err != nil {
		return nil, "", err
	}
	lines, next, err := bk.Bank.Recent(ctx, nuban, limit, cursor)
	if err != nil {
		return nil, "", err
	}
	return entriesOf(lines), next, nil
}

func (s *Banking) Entry(ctx context.Context, bankID, nuban, id string) (control.Entry, error) {
	bk, err := s.bankBook(bankID)
	if err != nil {
		return control.Entry{}, err
	}
	if _, err := s.customerAccount(ctx, bk.Bank, nuban); err != nil {
		return control.Entry{}, err
	}
	l, err := bk.Bank.Line(ctx, nuban, id)
	if err != nil {
		return control.Entry{}, err
	}
	return entryOf(l), nil
}

func (s *Banking) SessionEntries(ctx context.Context, bankID, sessionID string) ([]control.Entry, error) {
	bk, err := s.bankBook(bankID)
	if err != nil {
		return nil, err
	}
	lines, err := bk.Bank.SessionLines(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return entriesOf(lines), nil
}

func (s *Banking) HandleAccount(w http.ResponseWriter, r *http.Request) {
	acc, err := s.Account(r.Context(), s.bankIDOf(r), r.PathValue("nuban"))
	if err != nil {
		s.Error(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, acc)
}

func (s *Banking) HandleTotals(w http.ResponseWriter, r *http.Request) {
	t, err := s.Totals(r.Context(), s.bankIDOf(r))
	if err != nil {
		s.Error(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, t)
}

func (s *Banking) HandleEntries(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := ListParams(r)
	if err != nil {
		s.Error(w, r, err)
		return
	}
	data, next, err := s.Entries(r.Context(), s.bankIDOf(r), r.PathValue("nuban"), limit, cursor)
	if err != nil {
		s.Error(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, web.ListPage(data, next))
}

func (s *Banking) HandleEntry(w http.ResponseWriter, r *http.Request) {
	e, err := s.Entry(r.Context(), s.bankIDOf(r), r.PathValue("nuban"), r.PathValue("entry_id"))
	if err != nil {
		s.Error(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, e)
}

func (s *Banking) HandleSessionEntries(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	if !nip.ValidSessionID(sid) {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "session_id must have 30 digits.")
		return
	}
	data, err := s.SessionEntries(r.Context(), s.bankIDOf(r), sid)
	if err != nil {
		s.Error(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, map[string]any{"data": data})
}
