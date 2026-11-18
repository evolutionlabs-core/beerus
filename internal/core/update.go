package core

import (
	"context"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func (a *App) reconcileNow(ctx context.Context) {
	a.recMu.Lock()
	a.reconcileChanges()
	a.pokePush()
	a.recMu.Unlock()
	a.registerDue(ctx)
}

func (a *App) reconcileChanges() {
	st, err := a.ctl.Status()
	if err != nil {
		a.log.Error("read control.json", "err", err)
		return
	}
	now := a.clock.Real()
	for _, r := range st.Resources {
		if r.Generation == r.ObservedGeneration || (r.NextAttemptAt != nil && now.Before(*r.NextAttemptAt)) {
			continue
		}
		switch {
		case r.Kind == setup.KindBank && (r.Phase == PhaseActive || r.Phase == PhaseSuspended):
			a.updateBank(st.Applied, r)
		case r.Kind == setup.KindPartner && r.Phase == PhaseActive:
			a.updatePartner(r.ID)
		}
	}
}

func (a *App) updateBank(applied setup.Spec, r setup.Resource) {
	sb, ok := bankIn(applied, r.ID)
	if !ok {
		return
	}
	want := setup.RuntimeBank(sb, r)
	a.lifeMu.Lock()
	b := a.cfg.Bank(r.ID)
	if b == nil {
		a.lifeMu.Unlock()
		return
	}
	a.mu.Lock()
	renamed := b.Name != want.Name
	restate := b.State != want.State
	relimit := b.NetDebitCap != want.NetDebitCap || b.Collateral != want.Collateral
	b.Name, b.State, b.NetDebitCap, b.Collateral, b.API = want.Name, want.State, want.NetDebitCap, want.Collateral, want.API
	a.mu.Unlock()
	if st := a.status[r.ID]; st != nil {
		st.Name = want.Name
	}
	current := *b
	a.lifeMu.Unlock()
	if renamed || restate || relimit {
		a.markStale(r.ID, r.Generation)
		a.updateWaiting(r.ID)
	}
	phase := current.RunningPhase()
	a.lifeMu.Lock()
	if st := a.status[r.ID]; st != nil {
		if st.Phase == PhaseClosing || st.Phase == PhaseTerminating {
			a.lifeMu.Unlock()
			return
		}
		st.Phase = phase
	}
	a.lifeMu.Unlock()
	if a.staleMember(r.ID) {
		return
	}
	a.recordPhase(r.ID, phase, r.Generation)
}

func (a *App) updateWaiting(id string) {
	now := a.clock.Real()
	a.recordBank(id, func(r *setup.Resource) bool {
		return setCondition(r, setup.Condition{Type: setup.ConditionProgressing, Status: "True", Reason: reasonRetrying, Message: "waiting for NIBSS to take the change", ChangedAt: now})
	})
}

func (a *App) updateFailed(id string, err error) {
	now := a.clock.Real()
	a.recordBank(id, func(r *setup.Resource) bool {
		r.Attempts++
		next := now.Add(clock.Backoff(r.Attempts))
		r.NextAttemptAt = &next
		noteRetry(r, "update", err, now)
		return true
	})
}

func (a *App) updatePartner(id string) {
	a.partnerMu.Lock()
	defer a.partnerMu.Unlock()
	st, err := a.ctl.Status()
	if err != nil {
		a.log.Error("read control.json", "err", err)
		return
	}
	r := setup.FindResource(&st, setup.KindPartner, id)
	sp, ok := partnerIn(st.Applied, id)
	if r == nil || r.Partner == nil || !ok {
		return
	}
	local, err := setup.LoadLocal(a.cfg.Dir)
	if err != nil {
		a.log.Error("read local.yaml", "err", err)
		return
	}
	a.replacePartner(id, setup.RuntimePartner(sp, *r.Partner, local.Partners[id]))
	gen := r.Generation
	if err := a.ctl.Update(func(st *setup.Status) (bool, error) {
		cur := setup.FindResource(st, setup.KindPartner, id)
		if cur == nil || cur.ObservedGeneration == gen {
			return false, nil
		}
		cur.ObservedGeneration = gen
		return true, nil
	}); err != nil {
		a.log.Error("record partner update", "partner", id, "err", err)
	}
}
