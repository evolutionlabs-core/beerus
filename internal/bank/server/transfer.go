package server

import (
	"context"
	"errors"
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"github.com/evolutionlabs-core/beerus/internal/money"
	"math"
	"net/http"
	"strings"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/charges"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nip"
)

var (
	ErrSameAccount = errors.New("the from and to accounts are the same")
)

type Route struct {
	FromBank string
	ToBank   string
	FromName string
	ToName   string
	FromCode string
	ToCode   string
}

const nubanDigits = 10

func CheckRequest(req control.TransferRequest) error {
	if req.Amount <= 0 {
		return control.Refused(http.StatusBadRequest, nip.SystemError, "invalid_request", errors.New("amount must be above zero"))
	}
	if len(req.To) != nubanDigits || strings.Trim(req.To, "0123456789") != "" {
		return control.Refused(http.StatusUnprocessableEntity, nip.InvalidAccount, "invalid_account", fmt.Errorf("that is not a valid account number, so nothing was debited"))
	}
	if err := bank.CheckNarration(req.Narration); err != nil {
		return control.Refused(http.StatusUnprocessableEntity, nip.SystemError, "invalid_request", err)
	}
	return nil
}

func NewRoute(req control.TransferRequest, from, to *config.Bank, fromPhase, toPhase string) (Route, error) {
	if from == nil {
		return Route{}, control.Refused(http.StatusNotFound, nip.SystemError, "not_found", fmt.Errorf("no bank %q", req.FromBank))
	}
	if to == nil {
		return Route{}, control.Refused(http.StatusUnprocessableEntity, nip.SystemError, "unknown_bank", fmt.Errorf("find bank %q: %w", req.ToBank, nip.ErrUnknownInstitution))
	}
	if fromPhase != config.PhaseActive && !req.Closure {
		return Route{}, control.Refused(http.StatusConflict, nip.Unavailable, "bank_not_active", control.NotActiveError{Name: from.Name, Phase: fromPhase})
	}
	if toPhase == config.PhaseClosed {
		return Route{}, control.Refused(http.StatusUnprocessableEntity, nip.Unavailable, "bank_closed", fmt.Errorf("%s is closed and takes no more transfers. Nothing was debited", to.Name))
	}
	return Route{FromBank: from.ID, ToBank: to.ID, FromName: from.Name, ToName: to.Name, FromCode: from.NIPCode, ToCode: to.NIPCode}, nil
}

func overflowMessage() string {
	return fmt.Sprintf("this would take the balance past the largest amount an account can hold, %s. Use a smaller amount", money.Naira(math.MaxInt64))
}

func AccountError(side, nuban string, err error) error {
	if errors.Is(err, ledger.ErrUnknownAccount) {
		return control.Refused(http.StatusUnprocessableEntity, nip.InvalidAccount, "invalid_account", fmt.Errorf("the %s account %s does not exist", side, nuban))
	}
	return err
}

func PostingError(nuban string, err error) error {
	switch {
	case errors.Is(err, ledger.ErrUnknownAccount):
		return AccountError("from", nuban, err)
	case errors.Is(err, ledger.ErrInsufficientFunds):
		return control.Refused(http.StatusUnprocessableEntity, nip.NoFunds, "insufficient_funds", fmt.Errorf("account %s does not have enough money for this transfer and its charges", nuban))
	case errors.Is(err, ledger.ErrAccountClosed), errors.Is(err, ledger.ErrAccountFrozen), errors.Is(err, ledger.ErrPostNoDebit):
		return control.Refused(http.StatusUnprocessableEntity, nip.Restricted, "account_restricted", fmt.Errorf("account %s cannot be debited: %w", nuban, err))
	case errors.Is(err, ledger.ErrDailyLimit):
		return control.Refused(http.StatusUnprocessableEntity, nip.OverCap, "daily_limit", fmt.Errorf("account %s has reached its daily limit", nuban))
	case errors.Is(err, ledger.ErrOverflow):
		return control.Refused(http.StatusUnprocessableEntity, nip.SystemError, "invalid_amount", errors.New(overflowMessage()))
	case errors.Is(err, ledger.ErrIdempotency):
		return control.Refused(http.StatusUnprocessableEntity, nip.SystemError, "idempotency_key_reused", errors.New("that idempotency key was used for a different transfer"))
	}
	return err
}

func TotalDebit(amount int64, c charges.Transfer) (int64, error) {
	total, ok := bank.TotalDebit(amount, c)
	if !ok {
		return 0, control.Refused(http.StatusUnprocessableEntity, nip.SystemError, "amount_too_large", fmt.Errorf("%s is too large to add the transfer charges to. Nothing was debited", money.Naira(amount)))
	}
	return total, nil
}

type enquirer interface {
	Enquire(ctx context.Context, rt Route, nuban string) (string, error)
}

type Quoter struct {
	Fees     *charges.Schedule
	Clock    *clock.Clock
	Enquirer enquirer
}

func (q Quoter) Quote(ctx context.Context, b *bank.Bank, rt Route, req control.TransferRequest) (control.TransferResult, error) {
	now := q.Clock.Now()
	res := control.TransferResult{FromBank: rt.FromBank, From: req.From, ToBank: rt.ToBank, To: req.To, Amount: req.Amount, At: now}
	sender, err := b.L.Account(ctx, bank.CustomerAccount(req.From))
	if err != nil {
		return res, AccountError("from", req.From, err)
	}
	if rt.FromBank == rt.ToBank {
		if req.From == req.To {
			return res, control.Refused(http.StatusUnprocessableEntity, nip.SystemError, "invalid_request", ErrSameAccount)
		}
		to, err := b.L.Account(ctx, bank.CustomerAccount(req.To))
		if err != nil {
			return res, AccountError("to", req.To, err)
		}
		res.Kind, res.ToName, res.TotalDebit = "internal", to.Name, req.Amount
	} else {
		name, err := q.Enquirer.Enquire(ctx, rt, req.To)
		if err != nil {
			return res, err
		}
		c := q.Fees.ForTransfer(req.Amount)
		res.Kind, res.ToName = "nip", name
		res.Fee, res.VAT, res.StampDuty = c.Fee, c.VAT, c.StampDuty
		if res.TotalDebit, err = TotalDebit(req.Amount, c); err != nil {
			return res, err
		}
	}
	bal, err := b.BalanceAt(ctx, req.From, now)
	if err != nil {
		return res, fmt.Errorf("read balance of %s: %w", req.From, err)
	}
	if room, ok := money.AddMinor(bal, sender.OverdraftLimit); ok && room < res.TotalDebit {
		return res, PostingError(req.From, ledger.ErrInsufficientFunds)
	}
	res.BalanceAfter = bal - res.TotalDebit
	return res, nil
}
