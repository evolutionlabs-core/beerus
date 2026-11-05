package bank

import (
	"context"
	"errors"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/clock"
)

var errNotBuilt = errors.New("not built")

const (
	CustomerActive = "active"
	CustomerClosed = "closed"
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

func (b *Bank) CreateCustomer(ctx context.Context, key, name, phone string, dob clock.Date, bvn, username, passwordHash string, at time.Time) (Customer, error) {
	return Customer{}, errNotBuilt
}

func (b *Bank) CloseCustomer(ctx context.Context, id string, at time.Time) error {
	return errNotBuilt
}

func (b *Bank) OpenAccount(ctx context.Context, key, customer, product string, at time.Time) (ledger.Account, error) {
	return ledger.Account{}, errNotBuilt
}

func (b *Bank) CloseAccount(ctx context.Context, nuban string, at time.Time) error {
	return errNotBuilt
}

func (b *Bank) Customers(ctx context.Context, limit int, cursor string) ([]Customer, string, error) {
	return nil, "", errNotBuilt
}

func (b *Bank) Accounts(ctx context.Context, customer string, limit int, cursor string) ([]ledger.Account, string, error) {
	return nil, "", errNotBuilt
}

func (b *Bank) Statement(ctx context.Context, nuban string, from, to clock.Date, limit int, cursor string) ([]ledger.Event, string, error) {
	return nil, "", errNotBuilt
}

func (b *Bank) NameEnquiry(ctx context.Context, nuban string) (string, error) {
	return "", errNotBuilt
}

func (b *Bank) Authenticate(ctx context.Context, username, password string) (Customer, error) {
	return Customer{}, errNotBuilt
}
