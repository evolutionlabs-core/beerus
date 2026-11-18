package core

import (
	"context"
	"errors"
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"slices"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/state"
)

func (a *App) syncDirectoryDue(ctx context.Context) {
	a.mu.Lock()
	due := !a.dirSynced && !a.clock.Real().Before(a.dirNext)
	a.mu.Unlock()
	if !due {
		return
	}
	err := a.syncDirectory(ctx)
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.dirAttempts++
		a.dirNext = a.clock.Real().Add(clock.Backoff(a.dirAttempts))
		a.logNIBSS("NIBSS directory out of date; retrying", err)
		return
	}
	a.dirSynced, a.dirAttempts = true, 0
}

func (a *App) logNIBSS(msg string, err error) {
	var down *control.NIBSSDownError
	if errors.As(err, &down) {
		a.log.Warn(msg, "err", err)
		return
	}
	a.log.Error(msg, "err", err)
}

func (a *App) syncDirectory(ctx context.Context) error {
	list, err := a.nibssCtl.Directory(ctx)
	if err != nil {
		return fmt.Errorf("read the NIBSS directory: %w", err)
	}
	a.mu.Lock()
	banks := make([]config.Bank, 0, len(a.cfg.Banks))
	for _, b := range a.cfg.Banks {
		banks = append(banks, *b)
	}
	a.mu.Unlock()
	member := map[string]bool{}
	var errs []error
	for _, b := range banks {
		member[b.ID] = true
		errs = append(errs, a.registerMember(ctx, b))
	}
	for _, in := range list {
		if in.Status == nip.StatusRemoved || member[in.ID] {
			continue
		}
		for _, next := range []string{nip.StatusTerminating, nip.StatusRemoved} {
			if in.Status == next {
				continue
			}
			if err := a.nibssCtl.setStatus(ctx, in.ID, next); err != nil {
				errs = append(errs, fmt.Errorf("set NIBSS status of %s to %s: %w", in.ID, next, err))
				break
			}
		}
	}
	return errors.Join(errs...)
}

func memberStatus(b *config.Bank) string {
	if b.Suspended() || b.Closed() {
		return nip.StatusSuspended
	}
	return nip.StatusActive
}

func (a *App) registerMember(ctx context.Context, b config.Bank) error {
	in, err := a.nibssCtl.register(ctx, b)
	if err != nil {
		return fmt.Errorf("register %s with NIBSS: %w", b.ID, err)
	}
	if in.Status != nip.StatusTerminating && in.Status != memberStatus(&b) {
		if err := a.nibssCtl.setStatus(ctx, b.ID, memberStatus(&b)); err != nil {
			return fmt.Errorf("set NIBSS status of %s: %w", b.ID, err)
		}
	}
	if err := a.ctl.Update(func(st *setup.Status) (bool, error) {
		r := setup.FindResource(st, setup.KindBank, b.ID)
		if r == nil || r.Steps[config.StepDirectory] == state.StepDone {
			return false, nil
		}
		if r.Steps == nil {
			r.Steps = map[string]string{}
		}
		r.Steps[config.StepDirectory] = state.StepDone
		return true, nil
	}); err != nil {
		return err
	}
	return nil
}

func (a *App) stopRouting(ctx context.Context, r setup.Resource) (*setup.Condition, error) {
	in, err := a.nibssCtl.member(ctx, r.ID)
	if err != nil {
		return nil, fmt.Errorf("read back the NIBSS status of %s: %w", r.ID, err)
	}
	if in.Status != nip.StatusTerminating {
		return nil, fmt.Errorf("NIBSS shows %s as %s, not %s", r.ID, in.Status, nip.StatusTerminating)
	}
	return nil, nil
}

func (a *App) memberGone(ctx context.Context, id string) error {
	cur, err := a.memberStatusAt(ctx, id)
	if err != nil {
		return err
	}
	if cur != "" && cur != nip.StatusRemoved {
		return fmt.Errorf("NIBSS shows %s as %s, not %s", id, cur, nip.StatusRemoved)
	}
	return nil
}

func (a *App) memberStatusAt(ctx context.Context, id string) (string, error) {
	in, err := a.nibssCtl.member(ctx, id)
	if errors.Is(err, nip.ErrUnknownInstitution) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read the NIBSS directory: %w", err)
	}
	return in.Status, nil
}

func (a *App) driveMember(ctx context.Context, id string, path []string) error {
	cur, err := a.memberStatusAt(ctx, id)
	if err != nil {
		return err
	}
	for _, status := range path {
		if cur == nip.StatusRemoved || cur == "" {
			break
		}
		if cur == status {
			continue
		}
		if err := a.nibssCtl.setStatus(ctx, id, status); err != nil {
			return fmt.Errorf("set NIBSS status of %s to %s: %w", id, status, err)
		}
		cur = status
	}
	return nil
}

func (a *App) closingCallsDue(ctx context.Context) {
	st, err := a.ctl.Status()
	if err != nil {
		a.log.Error("read control.json", "err", err)
		return
	}
	now := a.clock.Real()
	for _, r := range st.Resources {
		if r.Kind != setup.KindBank || (r.Phase != config.PhaseClosing && r.Phase != config.PhaseTerminating) || (r.NextAttemptAt != nil && now.Before(*r.NextAttemptAt)) {
			continue
		}
		var path []string
		switch nextFinalizer(r) {
		case setup.FinalizerStopRouting:
			path = []string{nip.StatusTerminating}
		case setup.FinalizerPurgeOwned:
			path = []string{nip.StatusTerminating, nip.StatusRemoved}
		default:
			continue
		}
		if err := a.driveMember(ctx, r.ID, path); err != nil {
			a.logNIBSS("bank closing not sent to NIBSS; retrying", err)
		}
	}
}

const removedSuffix = " (removed)"

func (a *App) markRemoved(ctx context.Context, lines []FeedLine) error {
	if !slices.ContainsFunc(lines, func(l FeedLine) bool { return l.Counterparty != nil }) {
		return nil
	}
	directory, err := a.nibssCtl.Directory(ctx)
	var down *control.NIBSSDownError
	if errors.As(err, &down) {
		a.logNIBSS("removed banks not marked", err)
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the NIBSS directory: %w", err)
	}
	removed := map[string]bool{}
	for _, in := range directory {
		removed[in.NIPCode] = in.Status == nip.StatusRemoved
	}
	for _, l := range lines {
		if cp := l.Counterparty; cp != nil && removed[cp.BankCode] && !strings.HasSuffix(cp.BankName, removedSuffix) {
			cp.BankName += removedSuffix
		}
	}
	return nil
}

type staleMember struct {
	generation int64
	version    int
	attempts   int
	next       time.Time
}

func (a *App) markStale(id string, generation int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stale == nil {
		a.stale = map[string]*staleMember{}
	}
	s := a.stale[id]
	if s == nil {
		s = &staleMember{}
		a.stale[id] = s
	}
	s.generation, s.attempts, s.next = max(s.generation, generation), 0, time.Time{}
	s.version++
}

func (a *App) staleMember(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stale[id] != nil
}

type provisionJob struct {
	b  config.Bank
	st *BankStatus
}

type staleJob struct {
	b          config.Bank
	generation int64
	version    int
}

func (a *App) registerDue(ctx context.Context) {
	a.syncDirectoryDue(ctx)
	a.closingCallsDue(ctx)
	a.sendsDue(ctx)
	a.closeMovesDue(ctx)
	now := a.clock.Real()
	var jobs []provisionJob
	var stale []staleJob
	a.lifeMu.Lock()
	a.mu.Lock()
	for id := range a.stale {
		if a.cfg.Bank(id) == nil {
			delete(a.stale, id)
		}
	}
	for _, b := range a.cfg.Banks {
		st := a.status[b.ID]
		if st != nil && (st.Phase == PhaseProvisioning || st.Phase == PhaseRepairing) && !st.Steps[stepIndex(config.StepDirectory)].Done && (st.NextAttemptAt == nil || !now.Before(*st.NextAttemptAt)) {
			jobs = append(jobs, provisionJob{*b, st})
		}
		if s := a.stale[b.ID]; s != nil && !now.Before(s.next) {
			stale = append(stale, staleJob{*b, s.generation, s.version})
		}
	}
	a.mu.Unlock()
	a.lifeMu.Unlock()
	for _, j := range jobs {
		a.step(j.st, config.StepDirectory, func() error { return a.registerMember(ctx, j.b) })
	}
	a.cbnDue(ctx)
	for _, j := range stale {
		a.finishStale(j, a.registerMember(ctx, j.b))
	}
}

func (a *App) finishStale(j staleJob, err error) {
	a.mu.Lock()
	s := a.stale[j.b.ID]
	if s == nil {
		a.mu.Unlock()
		return
	}
	if err != nil {
		s.attempts++
		s.next = a.clock.Real().Add(clock.Backoff(s.attempts))
		a.mu.Unlock()
		a.logNIBSS("bank not updated at NIBSS; retrying", err)
		a.updateFailed(j.b.ID, err)
		return
	}
	current := s.version == j.version
	if current {
		delete(a.stale, j.b.ID)
	}
	a.mu.Unlock()
	if !current {
		return
	}
	a.lifeMu.Lock()
	st := a.status[j.b.ID]
	activating := st != nil && (st.Phase == PhaseProvisioning || st.Phase == PhaseRepairing)
	a.lifeMu.Unlock()
	if activating {
		return
	}
	phase := j.b.RunningPhase()
	a.recordBank(j.b.ID, func(r *setup.Resource) bool {
		if r.Phase == PhaseClosing || r.Phase == PhaseTerminating || r.Phase == PhaseClosed {
			return false
		}
		r.Phase = phase
		r.ObservedGeneration = max(r.ObservedGeneration, j.generation)
		r.Attempts, r.NextAttemptAt = 0, nil
		clearCondition(r, setup.ConditionDegraded)
		clearRetrying(r)
		return true
	})
}
