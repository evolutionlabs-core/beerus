package server

import (
	"context"
	"errors"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

var errBooksNotOpen = errors.New("the bank database is not ready yet")

type Host interface {
	Bank() config.Bank
	Phase() (string, string)
	Books() (*bank.Bank, error)
	Refusing(id string) bool
	RefreshRegistry(ctx context.Context) error
	RefreshBanks(ctx context.Context) error
	Directory() []*config.Bank
	Lookup(id string) *config.Bank
	Quote(ctx context.Context, req TransferRequest) (TransferResult, error)
	Transfer(ctx context.Context, req TransferRequest) (TransferResult, error)
	Banking() *Banking
}

func (s *Server) Books() (*bank.Bank, error) {
	b := s.books.Load()
	if b == nil {
		return nil, errBooksNotOpen
	}
	return b, nil
}

var _ Host = (*Server)(nil)
