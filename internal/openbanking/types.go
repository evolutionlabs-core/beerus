package openbanking

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

type Status string

const (
	AwaitingAuthorisation Status = "AwaitingAuthorisation"
	Authorised            Status = "Authorised"
	Rejected              Status = "Rejected"
	Revoked               Status = "Revoked"
	Expired               Status = "Expired"
	Dormant               Status = "Dormant"
	ReconsentRequired     Status = "ReconsentRequired"
)

var Scopes = []string{"accounts", "balances", "transactions"}

var ErrNotFound = errors.New("not found")

const ViaAggregator = "aggregator"

type Consent struct {
	ID              string
	ClientID        string
	PartnerID       string
	PartnerName     string
	PartnerLegal    string
	Purpose         string
	Customer        string
	Status          Status
	Scopes          []string
	Accounts        []string
	TransactionFrom clock.Date
	Analytics       bool
	CreatedAt       time.Time
	AuthorisedAt    time.Time
	ExpiresAt       time.Time
	LastUsedAt      time.Time
	RevokedVia      string
}

type Token struct {
	Hash       string
	Kind       string
	ConsentID  string
	ClientID   string
	Thumbprint string
	Family     string
	Parent     string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	RotatedAt  time.Time
	Revoked    bool
}

type Access struct {
	ConsentID string
	Account   string
	At        time.Time
}

type Store interface {
	CreateConsent(ctx context.Context, c *Consent) error
	Consent(ctx context.Context, id string) (*Consent, error)
	UpdateConsent(ctx context.Context, c *Consent) error
	ConsentsFor(ctx context.Context, customer string) ([]*Consent, error)
	PutToken(ctx context.Context, t *Token) error
	Token(ctx context.Context, hash string) (*Token, error)
	MarkRotated(ctx context.Context, hash string, at time.Time) (bool, error)
	RevokeConsentTokens(ctx context.Context, consentID string) error
	RevokeChildren(ctx context.Context, parent string) error
	RevokeFamily(ctx context.Context, family string) error
	RecordAccess(ctx context.Context, a Access) error
	MarkUsed(ctx context.Context, id string, at time.Time) error
	AccessFor(ctx context.Context, customer string, limit int) ([]Access, error)
	Awaiting(ctx context.Context) ([]*Consent, error)
	Enqueue(ctx context.Context, e ConsentEvent, at time.Time) error
	EnqueueLedger(ctx context.Context, e ConsentEvent, at time.Time) error
	LedgerDue(ctx context.Context, now time.Time, limit int) ([]Event, error)
	Due(ctx context.Context, now time.Time, limit int) ([]Event, error)
	Delivered(ctx context.Context, id int64) error
	Retry(ctx context.Context, id int64, attempts int, next time.Time) error
	Ping(ctx context.Context) error
}

type Client struct {
	ID           string
	Name         string
	LegalName    string
	RegistryID   string
	CAC          string
	Role         string
	CertCN       string
	Keys         []*ecdsa.PublicKey
	RedirectURIs []string
}

type Registry interface {
	Client(id string) (*Client, bool)
	ClientByCert(cn string) (*Client, bool)
}

type ConsentEvent struct {
	ConsentID string
	Bank      string
	ClientID  string
	PartnerID string
	Status    Status
	Via       string
	At        time.Time
	Seq       int64
}

type Event struct {
	ConsentEvent
	ID       int64
	Attempts int
}

type Ledger interface {
	Record(ConsentEvent)
}

type AccountInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Currency string `json:"currency"`
}

type Balance struct {
	Ledger    int64
	Available int64
	Currency  string
}

type Txn struct {
	ID           string
	Pending      bool
	Amount       int64
	BookedAt     time.Time
	ValueDate    clock.Date
	Narration    string
	Counterparty *Counterparty
	SessionID    string
	BankRef      string
	Channel      string
	BalanceAfter int64
	Seq          int64
	HadPending   bool
}

type Counterparty struct {
	Name          string
	AccountNumber string
	BankCode      string
	BankName      string
}

type Data interface {
	Accounts(customer string) ([]AccountInfo, error)
	Account(nuban string) (AccountInfo, error)
	Balance(nuban string) (Balance, error)
	BalanceOn(nuban string, d clock.Date) (int64, error)
	Transactions(nuban string, from, to clock.Date) ([]Txn, error)
	Ping() error
}
