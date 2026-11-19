package control

import (
	"errors"
	"fmt"
	"time"
)

type TransferRequest struct {
	FromBank  string `json:"from_bank"`
	From      string `json:"from"`
	ToBank    string `json:"to_bank"`
	To        string `json:"to"`
	Amount    int64  `json:"amount_minor"`
	Narration string `json:"narration"`
	Key       string `json:"-"`
	Closure   bool   `json:"-"`
}

type TransferResult struct {
	Kind         string     `json:"kind"`
	FromBank     string     `json:"from_bank"`
	From         string     `json:"from"`
	ToBank       string     `json:"to_bank"`
	To           string     `json:"to"`
	ToName       string     `json:"to_name"`
	SessionID    string     `json:"session_id,omitempty"`
	Status       string     `json:"status"`
	ResponseCode string     `json:"response_code"`
	Amount       int64      `json:"amount_minor"`
	Fee          int64      `json:"fee_minor"`
	VAT          int64      `json:"vat_minor"`
	StampDuty    int64      `json:"stamp_duty_minor"`
	TotalDebit   int64      `json:"total_debit_minor"`
	BalanceAfter int64      `json:"sender_balance_after_minor"`
	NextCheckAt  *time.Time `json:"next_check_at,omitempty"`
	At           time.Time  `json:"at"`
}

type TransferError struct {
	Code    string
	ErrCode string
	Status  int
	Err     error
}

func (e *TransferError) Error() string {
	return e.Err.Error()
}

func (e *TransferError) Unwrap() error {
	return e.Err
}

type TransferBody struct {
	From      string `json:"from" yaml:"from"`
	ToBank    string `json:"to_bank" yaml:"to_bank"`
	To        string `json:"to" yaml:"to"`
	Amount    string `json:"amount" yaml:"amount"`
	Narration string `json:"narration" yaml:"narration"`
	Closure   bool   `json:"closure,omitempty" yaml:"-"`
}

func Refused(status int, code, errCode string, err error) error {
	return &TransferError{Code: code, ErrCode: errCode, Status: status, Err: err}
}

type NotActiveError struct {
	Name  string
	Phase string
}

func (e NotActiveError) Error() string {
	return fmt.Sprintf("%s is %s, so it cannot do this now", e.Name, e.Phase)
}

func (NotActiveError) Is(target error) bool {
	return target == ErrBankNotActive
}

var ErrBankNotActive = errors.New("bank is not active")
