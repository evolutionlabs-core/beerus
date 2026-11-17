package connect

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
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
	if err := a.postBankSides(ctx); err != nil {
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
	return n, a.postBankSides(ctx)
}

func (a *App) postBankSides(ctx context.Context) error {
	sw := a.nibssStore()
	if sw == nil {
		return nil
	}
	a.mu.Lock()
	ledgers := slices.Clone(a.ledgers)
	a.mu.Unlock()
	var errs []error
	for _, l := range ledgers {
		errs = append(errs, postBankSide(ctx, sw, l))
	}
	return errors.Join(errs...)
}

func postBankSide(ctx context.Context, sw *nibss.Store, l *ledgerSource) error {
	b := l.Bank()
	posted, err := b.Settlements(ctx)
	if err != nil {
		return fmt.Errorf("read the settlements of %s: %w", l.Inst.ID, err)
	}
	list, err := sw.SettledExcept(ctx, l.Inst.NIPCode, slices.AppendSeq([]string{}, maps.Keys(posted)))
	if err != nil {
		return err
	}
	for _, x := range list {
		var p position
		for _, s := range x.Sessions {
			if s.FromCode == l.Inst.NIPCode {
				if p.outward > math.MaxInt64-s.Amount {
					return fmt.Errorf("settle %s at %s: the amount sent is too large to add", x.Key, l.Inst.ID)
				}
				p.outward += s.Amount
			}
			if s.ToCode == l.Inst.NIPCode {
				if p.inward > math.MaxInt64-s.Amount {
					return fmt.Errorf("settle %s at %s: the amount received is too large to add", x.Key, l.Inst.ID)
				}
				p.inward += s.Amount
			}
		}
		if _, err := b.Settle(ctx, x.Key, p.outward, p.inward, x.CutoffAt); err != nil {
			return fmt.Errorf("settle %s at %s: %w", x.Key, l.Inst.ID, err)
		}
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
	sw        *nibss.Store
	nibssNets map[string]map[string]int64
	open      []nibss.Session
}

func (a *App) settlementBooks(ctx context.Context) (settlementBooks, error) {
	sw := a.nibssStore()
	if sw == nil {
		return settlementBooks{}, errors.New("NIBSS is not open yet; try again soon")
	}
	nibssNets, err := sw.SettledNets(ctx)
	if err != nil {
		return settlementBooks{}, err
	}
	open, err := sw.Open(ctx)
	if err != nil {
		return settlementBooks{}, err
	}
	return settlementBooks{sw: sw, nibssNets: nibssNets, open: open}, nil
}

func (s settlementBooks) check(ctx context.Context, a *App, l *ledgerSource) ([]string, error) {
	var out []string
	code, id := l.Inst.NIPCode, l.Inst.ID
	b := l.Bank()
	posted, err := b.Settlements(ctx)
	if err != nil {
		return nil, err
	}
	keys := map[string]bool{}
	for k := range s.nibssNets[code] {
		keys[k] = true
	}
	for k := range posted {
		keys[k] = true
	}
	for _, k := range slices.Sorted(maps.Keys(keys)) {
		if n, p := s.nibssNets[code][k], posted[k]; n != p {
			out = append(out, fmt.Sprintf("session %s at %s: NIBSS nets %d, but the bank posted %d", k, id, n, p))
		}
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
	theirs, err := a.bankSettlementProblems(ctx, l)
	if err != nil {
		return nil, err
	}
	return append(out, theirs...), nil
}

func (a *App) bankSettlementProblems(ctx context.Context, l *ledgerSource) ([]string, error) {
	id := l.Inst.ID
	if a.role == RoleCore {
		b := a.cfg.Bank(id)
		if b == nil {
			return nil, fmt.Errorf("%s is not in the setup", id)
		}
		return a.freshBankProblems(ctx, b.URL(), id)
	}
	c, err := a.bankCBN(id)
	if err != nil {
		return nil, err
	}
	return server.SettlementProblems(ctx, c, l.Bank(), id)
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
