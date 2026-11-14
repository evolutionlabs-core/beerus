package store

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/charges"
	"github.com/evolutionlabs-core/beerus/internal/clock"
)

type Books struct {
	mu   sync.Mutex
	st   *Store
	b    *bank.Bank
	Inst bank.Profile
}

func OpenBooks(ctx context.Context, url, name string, inst bank.Profile, fees *charges.Schedule, genesisAt time.Time, migrations ...Migration) (*Books, error) {
	st, err := Open(ctx, url, name, migrations...)
	if err != nil {
		return nil, err
	}
	b := bank.New(ledger.New(st.Pool, nil), inst, fees)
	if err := b.Setup(ctx); err != nil {
		st.Close()
		return nil, err
	}
	if err := b.Genesis(ctx, bank.GenesisSettlement, bank.GenesisVault, genesisAt); err != nil {
		st.Close()
		return nil, err
	}
	return &Books{st: st, b: b, Inst: inst}, nil
}

func (s *Books) Pool() *pgxpool.Pool {
	return s.st.Pool
}

func (s *Books) Name() string {
	return s.st.Name()
}

func (s *Books) Bank() *bank.Bank {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b
}

func (s *Books) Fetch(ctx context.Context, nuban string, at time.Time, from clock.Date) ([]bank.FeedLine, error) {
	return s.Bank().Feed(ctx, nuban, at, from)
}

func (s *Books) LedgerAt(ctx context.Context, nuban string, at time.Time) (int64, error) {
	return s.Bank().BalanceAt(ctx, nuban, at)
}

func (s *Books) EndOfDay(ctx context.Context, nuban string, d clock.Date) (int64, error) {
	return s.LedgerAt(ctx, nuban, d.End().Add(-time.Nanosecond))
}

func (s *Books) Account(ctx context.Context, nuban string) (ledger.Account, error) {
	return s.Bank().L.Account(ctx, bank.CustomerAccount(nuban))
}

func (s *Books) Ping(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.Pool.Ping(ctx)
}

func (s *Books) Close() {
	s.st.Close()
}

func (s *Books) Drop(ctx context.Context) error {
	return s.st.Drop(ctx)
}
