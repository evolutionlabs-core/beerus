package connect

import (
	"context"
	"errors"
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/web"
	"net/http"
	"slices"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

const (
	bankCheckWait = 15 * time.Second
	bankCheckPoll = 200 * time.Millisecond
)

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
	ids := make([]string, len(a.cfg.Banks))
	for i, b := range a.cfg.Banks {
		ids[i] = b.ID
	}
	a.mu.Unlock()
	if len(ids) == 0 {
		return out, nil
	}
	if a.nibssCtl == nil {
		return out, &server.UnavailableError{Msg: nibssAway}
	}
	if _, err := a.settleRun(ctx, false); err != nil {
		return out, err
	}
	posted, problems, err := a.nibssCtl.status(ctx)
	if err != nil {
		return out, err
	}
	out.Sessions = posted
	out.Problems = append(out.Problems, problems...)
	cbnProblems, err := a.cbnCtl.problems(ctx)
	if err != nil {
		return out, err
	}
	out.Problems = append(out.Problems, cbnProblems...)
	for _, id := range ids {
		if a.bankGone(id) {
			continue
		}
		theirs, err := a.bankSettlementProblems(ctx, id)
		if err != nil {
			return out, err
		}
		out.Problems = append(out.Problems, theirs...)
	}
	return out, nil
}

func (a *App) bankSettlementProblems(ctx context.Context, id string) ([]string, error) {
	if a.role == RoleCore {
		b := a.bankByID(id)
		if b == nil {
			return nil, fmt.Errorf("%s is not in the setup", id)
		}
		return a.freshBankProblems(ctx, b)
	}
	a.mu.Lock()
	ls := a.ledgerFor(id)
	a.mu.Unlock()
	if ls == nil {
		return nil, fmt.Errorf("the books of %s are not open", id)
	}
	n, err := a.bankNIBSS(id)
	if err != nil {
		return nil, err
	}
	c, err := a.bankCBN(id)
	if err != nil {
		return nil, err
	}
	check, err := server.CheckBooks(ctx, n, c, ls.Bank(), id, a.clock.Now())
	if err != nil {
		return nil, err
	}
	var refused *server.BookingError
	if check.Booking != nil && !errors.As(check.Booking, &refused) {
		return nil, fmt.Errorf("settle %s: %w", id, check.Booking)
	}
	return check.Problems, nil
}

type uncheckedError struct {
	Name string
	Why  string
	Err  error
}

func (e *uncheckedError) Error() string {
	return e.Name + " " + e.Why
}

func (e *uncheckedError) message() string {
	return web.Sentence(e.Name+" "+e.Why) + " Try again soon."
}

func (e *uncheckedError) Unwrap() error {
	return e.Err
}

func (a *App) freshBankProblems(ctx context.Context, b *config.Bank) ([]string, error) {
	after := a.clock.Real()
	deadline := time.NewTimer(bankCheckWait)
	defer deadline.Stop()
	for {
		var state server.State
		if _, err := a.link.Do(ctx, b.URL(), http.MethodGet, "/v1/status", nil, &state); err != nil {
			if server.Unreachable(err) {
				return nil, &uncheckedError{Name: b.Name, Why: "is not answering, so its books were not checked", Err: err}
			}
			return nil, fmt.Errorf("read the status of %s: %w", b.ID, err)
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
			return nil, &uncheckedError{Name: b.Name, Why: "has not compared its books with the CBN yet"}
		case <-retry.C:
		}
	}
}
