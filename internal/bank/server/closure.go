package server

import (
	"context"
	"errors"
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

var errBankNotClosing = errors.New("bank is not closing")

type NotClosingError struct {
	Name  string
	Phase string
}

func (e NotClosingError) Error() string {
	return fmt.Sprintf("%s is %s, so it cannot pay out or freeze accounts; that happens only while a bank is closing", e.Name, e.Phase)
}

func (NotClosingError) Is(target error) bool {
	return target == errBankNotClosing
}

type ClosurePayoutResult struct {
	Account  string    `json:"account_number"`
	Amount   int64     `json:"amount_minor"`
	Balance  int64     `json:"balance_minor"`
	Replayed bool      `json:"replayed"`
	PostedAt time.Time `json:"posted_at"`
}

func (s *Banking) closingBook(id string) (Book, error) {
	bk, err := s.bankBook(id)
	if err != nil {
		return bk, err
	}
	if bk.Phase != config.PhaseClosing {
		return bk, NotClosingError{bk.Name, bk.Phase}
	}
	return bk, nil
}

func (s *Banking) ClosurePayout(ctx context.Context, bankID string, req control.ClosurePayoutRequest) (ClosurePayoutResult, error) {
	bk, err := s.closingBook(bankID)
	if err != nil {
		return ClosurePayoutResult{}, err
	}
	if req.Account == "" {
		return ClosurePayoutResult{}, fmt.Errorf("%w: the account number is empty", ledger.ErrInvalid)
	}
	if req.Amount <= 0 {
		return ClosurePayoutResult{}, fmt.Errorf("%w: the amount must be above zero, got %d", ledger.ErrInvalid, req.Amount)
	}
	p, err := bk.Bank.ClosurePayoutWithin(ctx, writeKey(req.Key, "clp"), req.Account, req.Amount, s.Clock.Now())
	if err != nil {
		return ClosurePayoutResult{}, err
	}
	bal, ok := p.Balances[bank.CustomerAccount(req.Account)]
	if !ok {
		return ClosurePayoutResult{}, fmt.Errorf("read balance of %s: the posted entry has no balance for it", req.Account)
	}
	return ClosurePayoutResult{Account: req.Account, Amount: req.Amount, Balance: bal, Replayed: p.Replayed, PostedAt: p.PostedAt.In(clock.Lagos)}, nil
}

func (s *Banking) ClosurePayouts(ctx context.Context, bankID string, limit int, cursor string) ([]control.ClosurePaid, string, error) {
	bk, err := s.bankBook(bankID)
	if err != nil {
		return nil, "", err
	}
	paid, err := bk.Bank.ClosurePayouts(ctx)
	if err != nil {
		return nil, "", err
	}
	nubans := make([]string, 0, len(paid))
	for nuban := range paid {
		nubans = append(nubans, nuban)
	}
	sort.Strings(nubans)
	start := 0
	if cursor != "" {
		i, found := slices.BinarySearch(nubans, cursor)
		if !found {
			return nil, "", fmt.Errorf("%w: %q", bank.ErrBadCursor, cursor)
		}
		start = i + 1
	}
	end := min(start+limit, len(nubans))
	data := make([]control.ClosurePaid, 0, end-start)
	for _, nuban := range nubans[start:end] {
		data = append(data, control.ClosurePaid{Account: nuban, Paid: paid[nuban]})
	}
	if end < len(nubans) {
		return data, nubans[end-1], nil
	}
	return data, "", nil
}

func (s *Banking) Freeze(ctx context.Context, bankID, nuban string) (control.AccountJSON, error) {
	bk, err := s.closingBook(bankID)
	if err != nil {
		return control.AccountJSON{}, err
	}
	b := bk.Bank
	acc, err := s.customerAccount(ctx, b, nuban)
	if err != nil {
		return control.AccountJSON{}, err
	}
	if acc.Status == "closed" {
		return control.AccountJSON{}, ledger.ErrAccountClosed
	}
	if err := b.FreezeAccount(ctx, nuban); err != nil {
		return control.AccountJSON{}, err
	}
	acc.Status = "frozen"
	return accountJSON(ctx, b, acc)
}

func (s *Banking) HandleClosurePayout(w http.ResponseWriter, r *http.Request) {
	key, ok := web.IdempotencyKey(w, r)
	if !ok {
		return
	}
	var req control.ClosurePayoutRequest
	if err := web.DecodeBody(r, &req); err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", `Send {"account_number":"0123456789","amount_minor":500000}.`)
		return
	}
	req.Key = key
	res, err := s.ClosurePayout(r.Context(), s.bankIDOf(r), req)
	if err != nil {
		s.Error(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusCreated, res)
}

func (s *Banking) HandleClosurePayouts(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := ListParams(r)
	if err != nil {
		s.Error(w, r, err)
		return
	}
	data, next, err := s.ClosurePayouts(r.Context(), s.bankIDOf(r), limit, cursor)
	if err != nil {
		s.Error(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, web.ListPage(data, next))
}

func (s *Banking) HandleFreeze(w http.ResponseWriter, r *http.Request) {
	acc, err := s.Freeze(r.Context(), s.bankIDOf(r), r.PathValue("nuban"))
	if err != nil {
		s.Error(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, acc)
}
