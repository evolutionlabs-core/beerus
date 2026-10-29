package connect

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

type ledgerSource struct {
	mu    sync.Mutex
	ctx   context.Context
	st    *store.Store
	b     *bank.Bank
	w     *world.World
	inst  world.Institution
	marks map[string]time.Time
	cfg   *config.Config
}

func openLedgerSource(ctx context.Context, cfg *config.Config, b *config.Bank, w *world.World) (*ledgerSource, error) {
	inst, ok := w.Institution(b.ID)
	if !ok {
		return nil, fmt.Errorf("bank %q is not in the world", b.ID)
	}
	if err := cfg.Claim("database", b.Database, b.ID); err != nil {
		return nil, err
	}
	if err := cfg.Claim("port", fmt.Sprint(b.Port), b.ID); err != nil {
		return nil, err
	}
	st, err := store.Open(ctx, cfg.DatabaseURL, b.Database)
	if err != nil {
		return nil, err
	}
	s := &ledgerSource{ctx: ctx, st: st, w: w, inst: inst, cfg: cfg, marks: map[string]time.Time{}}
	if err := s.setup(); err != nil {
		st.Close()
		return nil, err
	}
	return s, nil
}

func (s *ledgerSource) setup() error {
	s.b = bank.New(ledger.New(s.st.Pool, nil), bank.Profile{ID: s.inst.ID, Name: s.inst.Name, NIPCode: s.inst.NIPCode, NUBANCode: s.inst.NUBANCode})
	if err := s.b.Setup(s.ctx); err != nil {
		return err
	}
	start := s.w.Config().Genesis.Start()
	if err := s.b.Genesis(s.ctx, 1000000000000, 200000000000, start.Add(-time.Hour)); err != nil {
		return err
	}
	for _, acc := range s.w.Accounts() {
		if acc.Institution != s.inst.ID {
			continue
		}
		if err := s.b.EnsureCustomer(s.ctx, acc.NUBAN, acc.Name, acc.Type, acc.Owner, acc.Opening, start.Add(-time.Second)); err != nil {
			return err
		}
	}
	s.marks = map[string]time.Time{}
	return nil
}

func (s *ledgerSource) Institution() string {
	return s.inst.ID
}

func (s *ledgerSource) Bank() *bank.Bank {
	return s.b
}

func (s *ledgerSource) account(key string) (world.Account, error) {
	acc, ok := s.w.Account(key)
	if !ok || acc.Institution != s.inst.ID {
		return world.Account{}, world.ErrUnknownAccount
	}
	return acc, nil
}

func (s *ledgerSource) Sync(at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, acc := range s.w.Accounts() {
		if acc.Institution != s.inst.ID {
			continue
		}
		if err := s.syncAccount(acc, at); err != nil {
			return err
		}
	}
	return nil
}

func (s *ledgerSource) syncAccount(acc world.Account, at time.Time) error {
	mark, ok := s.marks[acc.Key]
	if !ok {
		wm, err := s.b.Watermark(s.ctx, acc.NUBAN)
		if err != nil {
			return err
		}
		mark = wm
		if mark.IsZero() {
			mark = s.w.Config().Genesis.Start().Add(-time.Nanosecond)
		}
		s.marks[acc.Key] = mark
	}
	if at.Before(mark) {
		return nil
	}
	vs, err := s.w.Versions(acc.Key, mark.Add(-time.Nanosecond), at)
	if err != nil {
		return err
	}
	for _, v := range vs {
		l := bank.FeedLine{Key: v.Key, NUBAN: acc.NUBAN, Pending: v.Pending, Amount: v.Amount, At: v.At, BookedAt: v.BookedAt, ValueDate: v.ValueDate, Narration: v.Narration, SessionID: v.SessionID, BankRef: v.BankRef, Channel: v.Channel}
		if v.Counterparty != nil {
			l.Counterparty = &bank.Counterparty{Name: v.Counterparty.Name, AccountNumber: v.Counterparty.AccountNumber, BankCode: v.Counterparty.BankCode, BankName: v.Counterparty.BankName}
		}
		if err := s.b.RecordLine(s.ctx, l); err != nil {
			return fmt.Errorf("posting %s: %w", v.Key, err)
		}
	}
	if err := s.b.SetWatermark(s.ctx, acc.NUBAN, at); err != nil {
		return err
	}
	s.marks[acc.Key] = at
	return nil
}

func (s *ledgerSource) ready(key string, at time.Time) (world.Account, error) {
	acc, err := s.account(key)
	if err != nil {
		return acc, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return acc, s.syncAccount(acc, at)
}

func (s *ledgerSource) Fetch(account string, at time.Time, from clock.Date) ([]world.Presented, error) {
	acc, err := s.ready(account, at)
	if err != nil {
		return nil, err
	}
	rows, err := s.b.Feed(s.ctx, acc.NUBAN, at, from)
	if err != nil {
		return nil, err
	}
	out := make([]world.Presented, 0, len(rows))
	for _, r := range rows {
		p := world.Presented{Key: r.Key, Pending: r.Pending, HadPending: r.HadPending, Amount: r.Amount, At: r.At, BookedAt: r.BookedAt, ValueDate: r.ValueDate, Narration: r.Narration, SessionID: r.SessionID, BankRef: r.BankRef, Channel: r.Channel, BalanceAfter: r.BalanceAfter, Seq: r.Seq}
		if r.Counterparty != nil {
			p.Counterparty = &world.Counterparty{Name: r.Counterparty.Name, AccountNumber: r.Counterparty.AccountNumber, BankCode: r.Counterparty.BankCode, BankName: r.Counterparty.BankName}
		}
		out = append(out, p)
	}
	s.w.Decorate(account, out, at)
	return out, nil
}

func (s *ledgerSource) LedgerAt(account string, at time.Time) (int64, error) {
	acc, err := s.ready(account, at)
	if err != nil {
		return 0, err
	}
	return s.b.BalanceAt(s.ctx, acc.NUBAN, at)
}

func (s *ledgerSource) PendingAt(account string, at time.Time) (int64, error) {
	acc, err := s.ready(account, at)
	if err != nil {
		return 0, err
	}
	return s.b.PendingAt(s.ctx, acc.NUBAN, at)
}

func (s *ledgerSource) EndOfDay(account string, d clock.Date) (int64, error) {
	return s.LedgerAt(account, d.End().Add(-time.Nanosecond))
}

func snapID(id string) string {
	return strings.ToLower(strings.TrimPrefix(id, "snap_"))
}

func (s *ledgerSource) Snapshot(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.cfg.Claim("database_snapshot", s.st.Name()+"_snap_"+snapID(id), s.inst.ID); err != nil {
		return err
	}
	if err := s.st.Snapshot(s.ctx, snapID(id)); err != nil {
		return err
	}
	return s.reopen()
}

func (s *ledgerSource) Restore(id string, w *world.World) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.st.Restore(s.ctx, snapID(id)); err != nil {
		return err
	}
	s.w = w
	return s.reopen()
}

func (s *ledgerSource) reopen() error {
	s.b = bank.New(ledger.New(s.st.Pool, nil), bank.Profile{ID: s.inst.ID, Name: s.inst.Name, NIPCode: s.inst.NIPCode, NUBANCode: s.inst.NUBANCode})
	s.marks = map[string]time.Time{}
	return nil
}

func (a *App) DropBankDatabases() error {
	var errs []error
	for _, l := range a.ledgers {
		if err := l.Drop(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *ledgerSource) Close() {
	s.st.Close()
}

func (s *ledgerSource) Drop() error {
	if err := s.st.Drop(context.Background()); err != nil {
		return err
	}
	return s.cfg.ReleaseBank(s.inst.ID)
}

type BankCheck struct {
	Ledger       ledger.Check
	Mismatches   []string
	ChargeMonths map[string]int
}

func (a *App) VerifyBank() (BankCheck, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := BankCheck{ChargeMonths: map[string]int{}}
	now := a.clock.Now()
	for _, l := range a.ledgers {
		if err := l.Sync(now); err != nil {
			return out, err
		}
		c, err := l.b.L.Verify(l.ctx)
		if err != nil {
			return out, err
		}
		out.Ledger.TrialBalance += c.TrialBalance
		out.Ledger.BalanceMismatch = append(out.Ledger.BalanceMismatch, c.BalanceMismatch...)
		out.Ledger.EventMismatch = append(out.Ledger.EventMismatch, c.EventMismatch...)
		out.Ledger.UnbalancedEntries = append(out.Ledger.UnbalancedEntries, c.UnbalancedEntries...)
		for _, acc := range a.world.Accounts() {
			if acc.Institution != l.inst.ID {
				continue
			}
			feed, err := l.b.BalanceAt(l.ctx, acc.NUBAN, now)
			if err != nil {
				return out, err
			}
			book, err := l.b.L.Balance(l.ctx, bank.Customer(acc.NUBAN))
			if err != nil {
				return out, err
			}
			if feed != book {
				out.Mismatches = append(out.Mismatches, fmt.Sprintf("%s feed %d ledger %d", acc.NUBAN, feed, book))
			}
			rows, err := l.b.Feed(l.ctx, acc.NUBAN, now, a.world.Config().Genesis)
			if err != nil {
				return out, err
			}
			for _, r := range rows {
				if !r.Pending && strings.HasPrefix(strings.ToUpper(r.Narration), "SMS ALERT CHARGES") {
					out.ChargeMonths[acc.NUBAN+" "+r.BookedAt.In(clock.Lagos).Format("2006-01")]++
				}
			}
		}
	}
	return out, nil
}
