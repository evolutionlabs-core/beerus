package connect

import (
	"errors"
	"fmt"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/control"
)

const customerActive = "active"

var (
	errUnknownCustomer = errors.New("bank: unknown customer")
	errBadLogin        = errors.New("bank: wrong username or password")
	errBankCursor      = errors.New("bank: invalid cursor")
	errUnknownLine     = errors.New("bank: unknown statement line")
	errUnknownAccount  = errors.New("ledger: unknown account")
)

type Customer struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Phone     string     `json:"phone"`
	BVN       string     `json:"bvn"`
	Status    string     `json:"status"`
	Username  string     `json:"username,omitempty"`
	DOB       clock.Date `json:"dob"`
	CreatedAt time.Time  `json:"created_at"`
	ClosedAt  *time.Time `json:"closed_at,omitempty"`
}

type FeedLine struct {
	Key          string
	NUBAN        string
	Amount       int64
	BookedAt     time.Time
	ValueDate    clock.Date
	Narration    string
	Counterparty *control.Counterparty
	SessionID    string
	BankRef      string
	Channel      string
	BalanceAfter int64
	Seq          int64
}

type Totals struct {
	Customers int
	Accounts  int
	Deposits  int64
}

type Check struct {
	TrialBalance      int64
	BalanceMismatch   []string
	EventMismatch     []string
	UnbalancedEntries []int64
}

func (c Check) OK() bool {
	return c.TrialBalance == 0 && len(c.BalanceMismatch) == 0 && len(c.EventMismatch) == 0 && len(c.UnbalancedEntries) == 0
}

func (c Check) String() string {
	return fmt.Sprintf("trial balance %d, balance mismatches %v, event mismatches %v, unbalanced entries %v", c.TrialBalance, c.BalanceMismatch, c.EventMismatch, c.UnbalancedEntries)
}

type position struct {
	Outward int64
	Inward  int64
}

func (p position) NetDebit() int64 {
	return max(p.Outward-p.Inward, 0)
}

type ClosureSession struct {
	ID          string    `json:"session_id"`
	FromAccount string    `json:"from_account"`
	ToBankCode  string    `json:"to_bank_code"`
	ToAccount   string    `json:"to_account"`
	Amount      int64     `json:"amount_minor"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

type ConsentEvent struct {
	Seq         int64     `json:"seq"`
	Bank        string    `json:"bank"`
	Incarnation string    `json:"incarnation"`
	ConsentID   string    `json:"consent_id"`
	ClientID    string    `json:"client_id"`
	PartnerID   string    `json:"partner_id"`
	Status      string    `json:"status"`
	Via         string    `json:"via"`
	ChangedAt   time.Time `json:"changed_at"`
	SourceSeq   int64     `json:"source_seq"`
}
