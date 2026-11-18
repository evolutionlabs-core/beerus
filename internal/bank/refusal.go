package bank

import (
	"context"
	"errors"
	"fmt"
	"unicode"
	"unicode/utf8"

	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/charges"
)

const MaxNarrationRunes = 100

func CheckNarration(n string) error {
	if count := utf8.RuneCountInString(n); count > MaxNarrationRunes {
		return fmt.Errorf("narration has %d characters; the limit is %d", count, MaxNarrationRunes)
	}
	for _, r := range n {
		if unicode.IsControl(r) {
			return errors.New("narration must not contain control characters")
		}
	}
	return nil
}

func TotalDebit(amount int64, c charges.Transfer) (int64, bool) {
	total, ok := AddMinor(amount, c.Fee)
	if ok {
		total, ok = AddMinor(total, c.VAT)
	}
	if ok {
		total, ok = AddMinor(total, c.StampDuty)
	}
	return total, ok
}

type InsufficientFundsError struct {
	Account string
	Balance int64
	Amount  int64
}

func (e *InsufficientFundsError) Error() string {
	return ledger.ErrInsufficientFunds.Error()
}

func (e *InsufficientFundsError) Unwrap() error {
	return ledger.ErrInsufficientFunds
}

func (e *InsufficientFundsError) Message() string {
	return fmt.Sprintf("Account %s holds %s and %s was asked for; withdraw %s or less.", e.Account, FormatNaira(e.Balance), FormatNaira(e.Amount), FormatNaira(max(e.Balance, 0)))
}

type PayoutTooLargeError struct {
	Account string
	Balance int64
	Amount  int64
}

func (e *PayoutTooLargeError) Error() string {
	return ledger.ErrInsufficientFunds.Error()
}

func (e *PayoutTooLargeError) Unwrap() error {
	return ledger.ErrInsufficientFunds
}

func (e *PayoutTooLargeError) Message() string {
	return fmt.Sprintf("Account %s holds %s and a payout of %s was asked for; pay out %s or less.", e.Account, FormatNaira(e.Balance), FormatNaira(e.Amount), FormatNaira(max(e.Balance, 0)))
}

type DailyLimitError struct {
	Account string
	Limit   int64
	Used    int64
	Amount  int64
}

func (e *DailyLimitError) Error() string {
	return ledger.ErrDailyLimit.Error()
}

func (e *DailyLimitError) Unwrap() error {
	return ledger.ErrDailyLimit
}

func (e *DailyLimitError) Message() string {
	return fmt.Sprintf("Account %s has used %s of its %s daily limit and %s was asked for; withdraw %s or less today.", e.Account, FormatNaira(e.Used), FormatNaira(e.Limit), FormatNaira(e.Amount), FormatNaira(max(e.Limit-e.Used, 0)))
}

func (b *Bank) CashRefusal(ctx context.Context, account string, amount int64, err error) error {
	var limit *ledger.DailyLimitError
	switch {
	case errors.Is(err, ledger.ErrInsufficientFunds):
		bal, berr := b.L.Balance(ctx, CustomerAccount(account))
		if berr != nil {
			return fmt.Errorf("read balance of %s: %w", account, berr)
		}
		return &InsufficientFundsError{Account: account, Balance: bal, Amount: amount}
	case errors.As(err, &limit):
		return &DailyLimitError{Account: account, Limit: limit.Limit, Used: limit.Used, Amount: amount}
	}
	return err
}
