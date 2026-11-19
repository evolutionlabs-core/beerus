package core

import (
	"context"
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"github.com/evolutionlabs-core/beerus/internal/web"
	"net/http"
	"sync"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
)

const (
	bankCheckWait = 15 * time.Second
	bankCheckPoll = 200 * time.Millisecond
)

func (a *App) SettleNow(ctx context.Context) (int, error) {
	return a.settleRun(ctx, true)
}

func (a *App) settleRun(ctx context.Context, adhoc bool) (int, error) {
	return a.nibssCtl.settle(ctx, adhoc)
}

func (a *App) bankGone(id string) bool {
	if a.bankByID(id) == nil {
		return true
	}
	phase := a.bankPhase(id)
	return phase == PhaseClosed || phase == PhaseTerminating
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
		return out, &control.UnavailableError{Msg: nibssAway}
	}
	if _, err := a.settleRun(ctx, false); err != nil {
		return out, err
	}
	after := a.clock.Real()
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
	live := ids[:0:0]
	for _, id := range ids {
		if !a.bankGone(id) {
			live = append(live, id)
		}
	}
	theirs, err := a.banksSettlementProblems(ctx, live, after)
	if err != nil {
		return out, err
	}
	out.Problems = append(out.Problems, theirs...)
	return out, nil
}

func (a *App) banksSettlementProblems(ctx context.Context, ids []string, after time.Time) ([]string, error) {
	problems := make([][]string, len(ids))
	errs := make([]error, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			problems[i], errs[i] = a.bankSettlementProblems(ctx, id, after)
		}()
	}
	wg.Wait()
	out := []string{}
	for i := range ids {
		if errs[i] != nil {
			return nil, errs[i]
		}
		out = append(out, problems[i]...)
	}
	return out, nil
}

func (a *App) bankSettlementProblems(ctx context.Context, id string, after time.Time) ([]string, error) {
	b := a.bankByID(id)
	if b == nil {
		return nil, fmt.Errorf("%s is not in the setup", id)
	}
	return a.freshBankProblems(ctx, b, after)
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

func unreadable(b *config.Bank, err error) error {
	if control.Unreachable(err) {
		return &uncheckedError{Name: b.Name, Why: "is not answering, so its books were not checked", Err: err}
	}
	return fmt.Errorf("read the status of %s: %w", b.ID, err)
}

func (a *App) freshBankProblems(ctx context.Context, b *config.Bank, after time.Time) ([]string, error) {
	deadline := time.NewTimer(bankCheckWait)
	defer deadline.Stop()
	if _, err := a.link.Do(ctx, b.URL(), http.MethodPost, "/v1/settlement/check", nil, nil); err != nil {
		return nil, unreadable(b, err)
	}
	for {
		var state control.State
		if _, err := a.link.Do(ctx, b.URL(), http.MethodGet, "/v1/status", nil, &state); err != nil {
			return nil, unreadable(b, err)
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
