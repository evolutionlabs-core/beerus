package connect

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

const snapshotInfix = "_snap_"

type ledgerSource struct {
	*store.Books
	ctl *ctlStore
}

func profileOf(b *config.Bank) bank.Profile {
	return bank.Profile{ID: b.ID, Name: b.Name, NIPCode: b.NIPCode, NUBANCode: b.NUBANCode}
}

func openLedgerSource(ctx context.Context, cfg *config.Config, ctl *ctlStore, clk *clock.Clock, b *config.Bank) (*ledgerSource, error) {
	books, err := store.OpenBooks(ctx, cfg.DatabaseURL, b.Database, profileOf(b), cfg.Fees(), clk.Now(), openbanking.Migrate)
	if err != nil {
		return nil, err
	}
	return &ledgerSource{Books: books, ctl: ctl}, nil
}

func (s *ledgerSource) Drop(ctx context.Context) error {
	name := s.Name()
	if err := s.Books.Drop(ctx); err != nil {
		return err
	}
	return s.ctl.Release(setup.Owned{Kind: ownedDatabase, Name: name})
}

func (a *App) dropBankDatabase(ctx context.Context, name string) error {
	all, err := store.List(ctx, a.cfg.DatabaseURL, name)
	if err != nil {
		return fmt.Errorf("list the databases of %s: %w", name, err)
	}
	owned := slices.DeleteFunc(all, func(n string) bool { return n != name && !strings.HasPrefix(n, name+snapshotInfix) })
	if err := store.DropDatabases(ctx, a.cfg.DatabaseURL, owned); err != nil {
		return fmt.Errorf("drop database %s: %w", name, err)
	}
	return a.ctl.Release(setup.Owned{Kind: ownedDatabase, Name: name})
}

func (a *App) DropBankDatabases(ctx context.Context) error {
	var errs []error
	if a.role == RoleCore {
		for _, b := range a.bankList() {
			errs = append(errs, a.dropBankDatabase(ctx, b.Database))
		}
	}
	for _, l := range a.ledgers {
		if err := l.Drop(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	errs = append(errs, a.dropCBN(ctx), a.dropNIBSS(ctx))
	return errors.Join(errs...)
}

func (a *App) VerifyBank(ctx context.Context) (ledger.Check, error) {
	var out ledger.Check
	for _, b := range a.bankList() {
		books := a.booksOf(b.ID)
		if books == nil {
			if a.role == RoleCore && a.bankPhase(b.ID) != PhaseClosed && !a.bankRetired(b.ID) {
				return out, &bankNotReadyError{Name: b.Name}
			}
			continue
		}
		c, err := books.check(ctx)
		if err != nil {
			return out, fmt.Errorf("verify the books of %s: %w", b.ID, err)
		}
		out.TrialBalance += c.TrialBalance
		out.BalanceMismatch = append(out.BalanceMismatch, c.BalanceMismatch...)
		out.EventMismatch = append(out.EventMismatch, c.EventMismatch...)
		out.UnbalancedEntries = append(out.UnbalancedEntries, c.UnbalancedEntries...)
	}
	return out, nil
}
