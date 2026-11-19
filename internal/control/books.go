package control

import (
	"time"
)

type AccountJSON struct {
	NUBAN    string `json:"nuban"`
	Name     string `json:"name"`
	Customer string `json:"customer"`
	Type     string `json:"type"`
	Status   string `json:"status"`
	Currency string `json:"currency"`
	Balance  int64  `json:"balance_minor"`
}

type AccountRequest struct {
	Customer string `json:"customer"`
	Type     string `json:"type"`
	Key      string `json:"-"`
}

type CustomerRequest struct {
	Name     string `json:"name"`
	Phone    string `json:"phone"`
	DOB      string `json:"dob"`
	Username string `json:"username"`
	Password string `json:"password"`
	Key      string `json:"-"`
}

type Entry struct {
	ID           string        `json:"id"`
	Account      string        `json:"account"`
	Seq          int64         `json:"seq"`
	Amount       int64         `json:"amount_minor"`
	BalanceAfter int64         `json:"balance_after_minor"`
	PostedAt     time.Time     `json:"posted_at"`
	ValueDate    string        `json:"value_date"`
	Narration    string        `json:"narration"`
	Channel      string        `json:"channel,omitempty"`
	SessionID    string        `json:"session_id,omitempty"`
	Reference    string        `json:"reference"`
	Counterparty *Counterparty `json:"counterparty,omitempty"`
}

type Totals struct {
	Customers int   `json:"customers"`
	Accounts  int   `json:"accounts"`
	Deposits  int64 `json:"deposits_minor"`
}

type LedgerCheck struct {
	OK                bool     `json:"ok"`
	TrialBalance      int64    `json:"trial_balance_minor"`
	BalanceMismatches []string `json:"balance_mismatches"`
	EventMismatches   []string `json:"event_mismatches"`
	UnbalancedEntries []int64  `json:"unbalanced_entries"`
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type LoginResult struct {
	Customer string `json:"customer_id"`
}

type ClosurePaid struct {
	Account string `json:"account_number"`
	Paid    int64  `json:"paid_minor"`
}

type ClosurePayoutRequest struct {
	Account string `json:"account_number"`
	Amount  int64  `json:"amount_minor"`
	Key     string `json:"-"`
}

const NoBanksMessage = "there are no banks yet. Add one first with beerus bank create --name NAME"

type UnavailableError struct {
	Msg string
	Err error
}

func (e *UnavailableError) Error() string {
	return e.Msg
}

func (e *UnavailableError) Unwrap() error {
	return e.Err
}

type Counterparty struct {
	Name          string `json:"name"`
	AccountNumber string `json:"account_number"`
	BankCode      string `json:"bank_code"`
	BankName      string `json:"bank_name"`
}
