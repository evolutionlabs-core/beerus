package connect

import (
	"fmt"

	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func (a *App) reconcileNow() {
	a.recMu.Lock()
	defer a.recMu.Unlock()
	a.reconcileChanges()
}

func (a *App) reconcileChanges() {
	st, err := a.ctl.status()
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
	want := runtimeBank(sb, r)
	a.lifeMu.Lock()
	b := a.cfg.Bank(r.ID)
	if b == nil {
		a.lifeMu.Unlock()
		return
	}
	a.mu.Lock()
	renamed := b.Name != want.Name
	restate := b.State != want.State
	reload := renamed || b.API != want.API
	b.Name, b.State, b.NetDebitCap, b.Collateral, b.API = want.Name, want.State, want.NetDebitCap, want.Collateral, want.API
	a.mu.Unlock()
	if st := a.status[r.ID]; st != nil {
		st.Name = want.Name
	}
	a.lifeMu.Unlock()
	if renamed || restate {
		if err := a.registerMember(b); err != nil {
			a.updateFailed(r.ID, err)
			return
		}
	}
	if reload {
		a.reloadSite(b.ID)
		return
	}
	a.recordPhase(r.ID, runningPhase(b))
	a.lifeMu.Lock()
	if st := a.status[r.ID]; st != nil {
		st.Phase = runningPhase(b)
	}
	a.lifeMu.Unlock()
}

func (a *App) updateFailed(id string, err error) {
	now := a.clock.Real()
	a.recordBank(id, func(r *setup.Resource) bool {
		r.Attempts++
		next := now.Add(backoff(r.Attempts))
		r.NextAttemptAt = &next
		setCondition(r, setup.Condition{Type: setup.ConditionDegraded, Status: "True", Reason: setup.ReasonStepFailed, Message: fmt.Sprintf("update: %v", err), ChangedAt: now})
		return true
	})
}

func (a *App) reloadSite(id string) {
	a.lifeMu.Lock()
	b := a.cfg.Bank(id)
	a.lifeMu.Unlock()
	if b == nil {
		return
	}
	a.detach(b, setup.StepSite)
	a.lifeMu.Lock()
	if st := a.status[id]; st != nil {
		st.Phase = PhaseRepairing
		for _, step := range []string{setup.StepSite, setup.StepAPI} {
			if i := stepIndex(step); i >= 0 {
				st.Steps[i].Done, st.Steps[i].Error = false, ""
			}
		}
	}
	a.lifeMu.Unlock()
	a.recordBank(id, func(r *setup.Resource) bool {
		r.Phase = PhaseRepairing
		delete(r.Steps, setup.StepSite)
		delete(r.Steps, setup.StepAPI)
		return true
	})
}

func (a *App) updatePartner(id string) {
	a.partnerMu.Lock()
	defer a.partnerMu.Unlock()
	st, err := a.ctl.status()
	if err != nil {
		a.log.Error("read control.json", "err", err)
		return
	}
	r := findResource(&st, setup.KindPartner, id)
	sp, ok := partnerIn(st.Applied, id)
	if r == nil || r.Partner == nil || !ok {
		return
	}
	local, err := setup.LoadLocal(a.cfg.Dir)
	if err != nil {
		a.log.Error("read local.yaml", "err", err)
		return
	}
	a.replacePartner(id, runtimePartner(sp, *r.Partner, local.Partners[id]))
	gen := r.Generation
	if err := a.ctl.update(func(st *setup.Status) (bool, error) {
		cur := findResource(st, setup.KindPartner, id)
		if cur == nil || cur.ObservedGeneration == gen {
			return false, nil
		}
		cur.ObservedGeneration = gen
		return true, nil
	}); err != nil {
		a.log.Error("record partner update", "partner", id, "err", err)
	}
}
