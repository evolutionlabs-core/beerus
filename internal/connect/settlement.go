package connect

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
)

const (
	bankCheckWait = 15 * time.Second
	bankCheckPoll = 200 * time.Millisecond
)

type position struct {
	outward, inward int64
}

func (a *App) schedule() nibss.Schedule {
	return nibss.Schedule{Per: a.cfg.Sessions(), Cal: a.cfg.Calendar()}
}

func (a *App) settleDue(ctx context.Context) {
	if err := a.postBooks(ctx); err != nil {
		a.log.Error("NIP settlement of bank books", "err", err)
	}
}

func (a *App) SettleNow(ctx context.Context) (int, error) {
	return a.settleRun(ctx, true)
}

func (a *App) settleRun(ctx context.Context, adhoc bool) (int, error) {
	n, err := a.nibssCtl.settle(ctx, adhoc)
	if err != nil {
		return 0, err
	}
	return n, a.postBooks(ctx)
}

func (a *App) postBooks(ctx context.Context) error {
	if a.role == RoleCore {
		return nil
	}
	a.mu.Lock()
	ledgers := slices.Clone(a.ledgers)
	a.mu.Unlock()
	var errs []error
	for _, l := range ledgers {
		if a.bankGone(l.Inst.ID) {
			continue
		}
		n, err := a.bankNIBSS(l.Inst.ID)
		if err == nil {
			err = a.postBank(ctx, n, l)
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (a *App) bankGone(id string) bool {
	if a.bankByID(id) == nil {
		return true
	}
	phase := a.bankPhase(id)
	return phase == PhaseClosed || phase == PhaseTerminating
}

func (a *App) postBank(ctx context.Context, n server.NIBSSClient, l *ledgerSource) error {
	if err := server.PostSettlements(ctx, n, l.Bank(), a.clock.Now()); err != nil {
		return fmt.Errorf("settle %s: %w", l.Inst.ID, err)
	}
	return nil
}

type SettlementCheck struct {
	Sessions int      `json:"sessions"`
	Problems []string `json:"problems"`
}

func (a *App) CheckSettlement(ctx context.Context) (SettlementCheck, error) {
	out := SettlementCheck{Problems: []string{}}
	a.mu.Lock()
	ledgers := slices.Clone(a.ledgers)
	empty := len(a.cfg.Banks) == 0
	a.mu.Unlock()
	if empty {
		return out, nil
	}
	if _, err := a.settleRun(ctx, false); err != nil {
		return out, err
	}
	books, err := a.settlementBooks(ctx)
	if err != nil {
		return out, err
	}
	if out.Sessions, err = books.sw.PostedCount(ctx); err != nil {
		return out, err
	}
	cbnProblems, err := a.cbnCtl.problems(ctx)
	if err != nil {
		return out, err
	}
	out.Problems = append(out.Problems, cbnProblems...)
	for _, l := range ledgers {
		problems, err := books.check(ctx, a, l)
		if err != nil {
			return out, err
		}
		out.Problems = append(out.Problems, problems...)
	}
	return out, nil
}

type settlementBooks struct {
	sw   *nibss.Store
	open []nibss.Session
}

func (a *App) settlementBooks(ctx context.Context) (settlementBooks, error) {
	sw := a.nibssStore()
	if sw == nil {
		return settlementBooks{}, errors.New("NIBSS is not open yet; try again soon")
	}
	open, err := sw.Open(ctx)
	if err != nil {
		return settlementBooks{}, err
	}
	return settlementBooks{sw: sw, open: open}, nil
}

func (s settlementBooks) check(ctx context.Context, a *App, l *ledgerSource) ([]string, error) {
	var out []string
	code, id := l.Inst.NIPCode, l.Inst.ID
	b := l.Bank()
	theirs, err := a.bankSettlementProblems(ctx, l)
	if err != nil {
		return nil, err
	}
	var want position
	for _, x := range s.open {
		if x.FromCode == code {
			want.outward += x.Amount
		}
		if x.ToCode == code && x.Status == nibss.SessionOK {
			want.inward += x.Amount
		}
	}
	owe, err := b.L.Balance(ctx, bank.NIPOutward)
	if err != nil {
		return nil, err
	}
	due, err := b.L.Balance(ctx, bank.NIPInward)
	if err != nil {
		return nil, err
	}
	if owe != want.outward || due != want.inward {
		out = append(out, fmt.Sprintf("%s has NIP payable %d and receivable %d, but its open NIBSS sessions come to %d and %d", id, owe, due, want.outward, want.inward))
	}
	return append(out, theirs...), nil
}

func (a *App) bankSettlementProblems(ctx context.Context, l *ledgerSource) ([]string, error) {
	id := l.Inst.ID
	if a.role == RoleCore {
		b := a.bankByID(id)
		if b == nil {
			return nil, fmt.Errorf("%s is not in the setup", id)
		}
		if a.bankPhase(id) == PhaseClosed {
			return []string{}, nil
		}
		return a.freshBankProblems(ctx, b.URL(), id)
	}
	n, err := a.bankNIBSS(id)
	if err != nil {
		return nil, err
	}
	c, err := a.bankCBN(id)
	if err != nil {
		return nil, err
	}
	if err := a.postBank(ctx, n, l); err != nil {
		return nil, err
	}
	return server.SettlementProblems(ctx, n, c, l.Bank(), id)
}

func (a *App) freshBankProblems(ctx context.Context, base, id string) ([]string, error) {
	after := a.clock.Real()
	deadline := time.NewTimer(bankCheckWait)
	defer deadline.Stop()
	for {
		var state server.State
		if _, err := a.link.Do(ctx, base, http.MethodGet, "/v1/status", nil, &state); err != nil {
			return nil, fmt.Errorf("read the status of %s: %w", id, err)
		}
		if at := state.SettlementCheckedAt; at != nil && at.After(after) {
			return state.SettlementProblems, nil
		}
		retry := time.NewTimer(bankCheckPoll)
		select {
		case <-ctx.Done():
			retry.Stop()
			return nil, ctx.Err()
		case <-deadline.C:
			retry.Stop()
			return nil, fmt.Errorf("%s has not compared its books with the CBN yet; try again soon", id)
		case <-retry.C:
		}
	}
}
