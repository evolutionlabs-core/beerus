package connect

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

type ledgerSource struct {
	mu      sync.Mutex
	ctx     context.Context
	st      *store.Store
	b       *bank.Bank
	inst    bank.Profile
	start   time.Time
	cfg     *config.Config
	ctl     *ctlStore
	settled time.Time
}

func profileOf(b *config.Bank) bank.Profile {
	return bank.Profile{ID: b.ID, Name: b.Name, NIPCode: b.NIPCode, NUBANCode: b.NUBANCode}
}

func openLedgerSource(ctx context.Context, cfg *config.Config, ctl *ctlStore, b *config.Bank) (*ledgerSource, error) {
	st, err := store.Open(ctx, cfg.DatabaseURL, b.Database, openbanking.Migrate)
	if err != nil {
		return nil, err
	}
	s := &ledgerSource{ctx: ctx, st: st, inst: profileOf(b), start: cfg.Start, cfg: cfg, ctl: ctl}
	if err := s.setup(); err != nil {
		st.Close()
		return nil, err
	}
	return s, nil
}

func (s *ledgerSource) setup() error {
	s.b = bank.New(ledger.New(s.st.Pool, nil), s.inst, s.cfg.Fees())
	if err := s.b.Setup(s.ctx); err != nil {
		return err
	}
	if err := s.b.Genesis(s.ctx, bank.GenesisSettlement, bank.GenesisVault, s.start.Add(-time.Hour)); err != nil {
		return err
	}
	s.settled = time.Time{}
	return nil
}

func (s *ledgerSource) Institution() string {
	return s.inst.ID
}

func (s *ledgerSource) Bank() *bank.Bank {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b
}

func (s *ledgerSource) Fetch(nuban string, at time.Time, from clock.Date) ([]bank.FeedLine, error) {
	return s.Bank().Feed(s.ctx, nuban, at, from)
}

func (s *ledgerSource) LedgerAt(nuban string, at time.Time) (int64, error) {
	return s.Bank().BalanceAt(s.ctx, nuban, at)
}

func (s *ledgerSource) EndOfDay(nuban string, d clock.Date) (int64, error) {
	return s.LedgerAt(nuban, d.End().Add(-time.Nanosecond))
}

func (s *ledgerSource) Account(nuban string) (ledger.Account, error) {
	return s.Bank().L.Account(s.ctx, bank.CustomerAccount(nuban))
}

func snapID(id string) string {
	return strings.ToLower(strings.TrimPrefix(id, "snap_"))
}

func (s *ledgerSource) Snapshot(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ctl.Claim(setup.KindBank, s.inst.ID, setup.Owned{Kind: ownedSnapshot, Name: s.snapshotName(id)}); err != nil {
		return err
	}
	if err := s.st.Snapshot(s.ctx, snapID(id)); err != nil {
		return err
	}
	return s.reopen()
}

func (s *ledgerSource) DropSnapshot(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := s.snapshotName(id)
	if err := store.DropDatabases(s.ctx, s.st.URL(), []string{name}); err != nil {
		return err
	}
	return s.ctl.Release(setup.Owned{Kind: ownedSnapshot, Name: name})
}

func (s *ledgerSource) snapshotName(id string) string {
	return s.st.Name() + "_snap_" + snapID(id)
}

func (s *ledgerSource) HasSnapshot(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.HasSnapshot(s.ctx, snapID(id))
}

func (s *ledgerSource) Restore(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	serial, err := s.b.NUBANSerial(s.ctx)
	if err != nil {
		return err
	}
	if err := s.st.Restore(s.ctx, snapID(id)); err != nil {
		return err
	}
	if err := s.reopen(); err != nil {
		return err
	}
	return s.b.RaiseNUBANSerial(s.ctx, serial)
}

func (s *ledgerSource) Release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Pool.Close()
}

func (s *ledgerSource) Reconnect() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := store.Open(s.ctx, s.cfg.DatabaseURL, s.st.Name())
	if err != nil {
		return err
	}
	old := s.st
	s.st = st
	old.Close()
	return s.reopen()
}

func (s *ledgerSource) reopen() error {
	s.b = bank.New(ledger.New(s.st.Pool, nil), s.inst, s.cfg.Fees())
	s.settled = time.Time{}
	return nil
}

func (a *App) DropBankDatabases() error {
	var errs []error
	for _, l := range a.ledgers {
		if err := l.Drop(); err != nil {
			errs = append(errs, err)
		}
	}
	if a.rtgs != nil {
		errs = append(errs, a.rtgs.Drop())
	}
	errs = append(errs, a.dropNIBSS())
	return errors.Join(errs...)
}

func (s *ledgerSource) Ping(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.Pool.Ping(ctx)
}

func (s *ledgerSource) Close() {
	s.st.Close()
}

func (s *ledgerSource) Drop() error {
	name := s.st.Name()
	if err := s.st.Drop(context.Background()); err != nil {
		return err
	}
	return s.ctl.Release(setup.Owned{Kind: ownedDatabase, Name: name})
}

func (a *App) VerifyBank() (ledger.Check, error) {
	a.mu.Lock()
	ledgers := slices.Clone(a.ledgers)
	a.mu.Unlock()
	var out ledger.Check
	for _, l := range ledgers {
		c, err := l.Bank().L.Verify(l.ctx)
		if err != nil {
			return out, fmt.Errorf("verify the books of %s: %w", l.inst.ID, err)
		}
		out.TrialBalance += c.TrialBalance
		out.BalanceMismatch = append(out.BalanceMismatch, c.BalanceMismatch...)
		out.EventMismatch = append(out.EventMismatch, c.EventMismatch...)
		out.UnbalancedEntries = append(out.UnbalancedEntries, c.UnbalancedEntries...)
	}
	return out, nil
}
