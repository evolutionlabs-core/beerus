package core

import (
	"context"
	"errors"
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"slices"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/state"
)

func noError() error {
	return nil
}

func ensuredSteps() []string {
	return []string{config.StepDatabase, config.StepLedgerSchema, config.StepOpenBankingSchema}
}

func stepIndex(id string) int {
	return slices.IndexFunc(config.BankSteps(), func(s config.BankStep) bool { return s.ID == id })
}

func newStatus(b *config.Bank, done bool) *BankStatus {
	st := &BankStatus{ID: b.ID, Name: b.Name, Phase: PhaseProvisioning, URL: b.URL()}
	for _, s := range config.BankSteps() {
		name := s.Name
		if s.ID == config.StepAllocate && b.NIPCode != "" {
			name += " (NIP " + b.NIPCode + ", NUBAN " + b.NUBANCode + ")"
		}
		st.Steps = append(st.Steps, BankStep{ID: s.ID, Name: name, Done: done})
	}
	if done {
		st.Phase = b.RunningPhase()
	}
	if b.Closed() {
		st.Phase = PhaseClosed
	}
	return st
}

func (a *App) initLifecycle() {
	a.status = map[string]*BankStatus{}
	a.strikes = map[string]int{}
	stored, err := a.ctl.Status()
	if err != nil {
		a.log.Error("read control.json", "err", err)
	}
	for _, b := range a.cfg.Banks {
		r := setup.FindResource(&stored, setup.KindBank, b.ID)
		st := newStatus(b, a.provisionedAtStart(b, r))
		a.status[b.ID] = st
		if r != nil && (r.Phase == PhaseClosing || r.Phase == PhaseTerminating) {
			st.Phase = r.Phase
			continue
		}
		if b.Closed() {
			continue
		}
		if r == nil || (r.Phase != PhaseProvisioning && r.Phase != PhaseRepairing) {
			continue
		}
		st.Phase = PhaseProvisioning
		if r != nil && r.Phase == PhaseRepairing {
			st.Phase = PhaseRepairing
		}
		for i := range st.Steps {
			st.Steps[i].Done = st.Steps[i].ID == config.StepAllocate && r != nil && r.Steps[config.StepAllocate] == state.StepDone
		}
	}
}

func (a *App) provisionedAtStart(b *config.Bank, r *setup.Resource) bool {
	return r != nil && r.Phase != PhaseProvisioning
}

func (a *App) recordBank(id string, fn func(r *setup.Resource) bool) {
	err := a.ctl.Update(func(st *setup.Status) (bool, error) {
		r := setup.FindResource(st, setup.KindBank, id)
		if r == nil {
			return false, nil
		}
		return fn(r), nil
	})
	if err != nil {
		a.log.Error("record bank status", "bank", id, "err", err)
	}
}

func (a *App) recordPhase(id, phase string, gen int64) {
	a.recordBank(id, func(r *setup.Resource) bool {
		if r.Phase == PhaseClosing || r.Phase == PhaseTerminating || r.Phase == PhaseClosed {
			return false
		}
		changed := r.Phase != phase
		r.Phase = phase
		if phase == PhaseActive || phase == PhaseSuspended {
			seen := max(r.ObservedGeneration, gen)
			changed = changed || r.ObservedGeneration != seen || r.Attempts != 0 || r.NextAttemptAt != nil
			r.ObservedGeneration, r.Attempts, r.NextAttemptAt = seen, 0, nil
			changed = clearCondition(r, setup.ConditionDegraded) || changed
			changed = clearRetrying(r) || changed
		}
		return changed
	})
}

func (a *App) step(st *BankStatus, id string, run func() error) bool {
	i := stepIndex(id)
	a.lifeMu.Lock()
	done := st.Steps[i].Done
	a.lifeMu.Unlock()
	if done {
		return true
	}
	started := a.clock.Real()
	a.lifeMu.Lock()
	st.Steps[i].StartedAt = &started
	a.lifeMu.Unlock()
	err := run()
	now := a.clock.Real()
	a.lifeMu.Lock()
	st.Steps[i].At = &now
	if err != nil {
		st.Steps[i].Error = err.Error()
		st.Attempts++
		next := now.Add(clock.Backoff(st.Attempts))
		st.NextAttemptAt = &next
	} else {
		st.Steps[i].Done, st.Steps[i].Error = true, ""
	}
	attempts, next := st.Attempts, st.NextAttemptAt
	a.lifeMu.Unlock()
	a.recordBank(st.ID, func(r *setup.Resource) bool {
		if r.Steps == nil {
			r.Steps = map[string]string{}
		}
		if err != nil {
			r.Steps[id] = err.Error()
			r.Attempts, r.NextAttemptAt = attempts, next
			return true
		}
		if r.Steps[id] == state.StepDone {
			return false
		}
		r.Steps[id] = state.StepDone
		return true
	})
	return err == nil
}

func (a *App) reconcileBanks(ctx context.Context) {
	a.checkDrift()
	now := a.clock.Real()
	stored, err := a.ctl.Status()
	if err != nil {
		a.log.Error("read control.json", "err", err)
	}
	a.lifeMu.Lock()
	pending := []*config.Bank{}
	for _, b := range a.cfg.Banks {
		st := a.status[b.ID]
		if st == nil || (st.Phase != PhaseProvisioning && st.Phase != PhaseRepairing && !a.needsRepairLocked(st, stored)) {
			continue
		}
		if st.NextAttemptAt != nil && now.Before(*st.NextAttemptAt) {
			continue
		}
		pending = append(pending, b)
	}
	a.lifeMu.Unlock()
	for _, b := range pending {
		a.provision(ctx, b)
	}
	a.reconcileChanges()
	a.advanceFinalizers(ctx, finalizerLists)
}

func (a *App) needsRepairLocked(st *BankStatus, stored setup.Status) bool {
	if st.Phase != PhaseClosing && st.Phase != PhaseTerminating {
		return false
	}
	r := setup.FindResource(&stored, setup.KindBank, st.ID)
	if r == nil || nextFinalizer(*r) == setup.FinalizerPurgeOwned || slices.Contains(r.Done, setup.FinalizerPurgeOwned) {
		return false
	}
	return slices.ContainsFunc(st.Steps, func(s BankStep) bool { return !s.Done })
}

func (a *App) bankGeneration(id string) int64 {
	st, err := a.ctl.Status()
	if err != nil {
		a.log.Error("read control.json", "err", err)
		return 0
	}
	if r := setup.FindResource(&st, setup.KindBank, id); r != nil {
		return r.Generation
	}
	return 0
}

func (a *App) provision(ctx context.Context, b *config.Bank) {
	gen := a.bankGeneration(b.ID)
	a.lifeMu.Lock()
	st := a.status[b.ID]
	a.lifeMu.Unlock()
	ok := a.step(st, config.StepAllocate, func() error {
		return a.claimBank(b)
	}) && a.stepDone(st, config.StepDirectory) && a.step(st, config.StepCerts, func() error {
		return a.bankCerts(b)
	}) && a.stepDone(st, config.StepCBN) && a.bankBooksReady(ctx, b, st) && a.step(st, config.StepSite, noError) && a.step(st, config.StepAPI, noError)
	if !ok {
		return
	}
	a.lifeMu.Lock()
	leaving := st.leavingLocked()
	a.lifeMu.Unlock()
	if leaving || a.staleMember(b.ID) || !a.takeConfirm(b.ID, gen) {
		return
	}
	phase := b.RunningPhase()
	a.recordPhase(b.ID, phase, gen)
	a.lifeMu.Lock()
	if st.leavingLocked() {
		a.lifeMu.Unlock()
		return
	}
	st.Phase, st.Reason, st.Attempts, st.NextAttemptAt = phase, "", 0, nil
	a.lifeMu.Unlock()
	a.pokePush()
}

func (a *App) bankBooksReady(ctx context.Context, b *config.Bank, st *BankStatus) bool {
	admin, err := a.adminOf(b.ID)
	if err != nil {
		return false
	}
	state, err := admin.state(ctx)
	if err != nil {
		return false
	}
	for _, id := range ensuredSteps() {
		i := slices.IndexFunc(state.Steps, func(s control.Step) bool { return s.ID == id })
		if i < 0 {
			return false
		}
		theirs := state.Steps[i]
		if !theirs.Done && theirs.Error == "" {
			return false
		}
		if !a.step(st, id, func() error {
			if !theirs.Done {
				return errors.New(theirs.Error)
			}
			a.copyStepName(st, id, theirs.Name)
			return nil
		}) {
			return false
		}
	}
	return true
}

func (a *App) copyStepName(st *BankStatus, id, name string) {
	i := stepIndex(id)
	a.lifeMu.Lock()
	defer a.lifeMu.Unlock()
	st.Steps[i].Name = name
}

func (st *BankStatus) leavingLocked() bool {
	return st.Phase == PhaseClosing || st.Phase == PhaseTerminating
}

func (a *App) stepDone(st *BankStatus, id string) bool {
	a.lifeMu.Lock()
	defer a.lifeMu.Unlock()
	return st.Steps[stepIndex(id)].Done
}

func (a *App) bankCerts(b *config.Bank) error {
	if _, err := a.ensureBankDir(b.ID, true); err != nil {
		return fmt.Errorf("issue certificates for %s: %w", b.ID, err)
	}
	return a.ctl.Claim(setup.KindBank, b.ID, setup.Owned{Kind: state.OwnedDir, Name: "banks/" + b.ID})
}

func driftRepairs(phase string, done []string, missing []string) (redo []string, newPhase string) {
	switch phase {
	case PhaseActive, PhaseSuspended:
		return missing, PhaseRepairing
	case PhaseClosing, PhaseTerminating:
		if slices.Contains(done, setup.FinalizerPurgeOwned) {
			return nil, phase
		}
		return missing, phase
	}
	return nil, phase
}

func (a *App) checkDrift() {
	if a.clock.Real().Sub(a.driftAt) < driftEvery && !a.driftDue.Swap(false) {
		return
	}
	a.driftAt = a.clock.Real()
	stored, err := a.ctl.Status()
	if err != nil {
		a.log.Error("read control.json", "err", err)
		return
	}
	a.lifeMu.Lock()
	serving := a.serving
	banks := []*config.Bank{}
	for _, b := range a.cfg.Banks {
		if st := a.status[b.ID]; st != nil && (st.Phase == PhaseActive || st.Phase == PhaseSuspended || st.Phase == PhaseClosing || st.Phase == PhaseTerminating) {
			banks = append(banks, b)
		}
	}
	a.lifeMu.Unlock()
	if !serving {
		return
	}
	for _, b := range banks {
		reason, missing := a.probe(b)
		if reason == "" {
			delete(a.strikes, b.ID)
			continue
		}
		if a.strikes[b.ID]++; a.strikes[b.ID] < 2 {
			a.driftDue.Store(true)
			continue
		}
		delete(a.strikes, b.ID)
		var done []string
		if r := setup.FindResource(&stored, setup.KindBank, b.ID); r != nil {
			done = r.Done
		}
		a.lifeMu.Lock()
		st := a.status[b.ID]
		redo, phase := driftRepairs(st.Phase, done, missing)
		a.lifeMu.Unlock()
		if len(redo) == 0 {
			continue
		}
		a.log.Warn("bank drifted; repairing", "bank", b.ID, "reason", reason)
		a.lifeMu.Lock()
		st.Phase, st.Reason = phase, reason
		for _, id := range redo {
			i := stepIndex(id)
			st.Steps[i].Done, st.Steps[i].Error = false, ""
		}
		a.lifeMu.Unlock()
		a.recordBank(b.ID, func(r *setup.Resource) bool {
			r.Phase = phase
			for _, id := range redo {
				delete(r.Steps, id)
			}
			return true
		})
	}
}

func (a *App) probe(b *config.Bank) (string, []string) {
	rebuild := []string{config.StepDatabase, config.StepLedgerSchema, config.StepOpenBankingSchema, config.StepSite, config.StepAPI}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if reason := a.probeBank(ctx, b); reason != "" {
		return reason, rebuild
	}
	if b.Closed() {
		return "", nil
	}
	resp, err := a.probeHTTP.Get(b.URL() + "/health")
	if err == nil {
		resp.Body.Close()
		return "", nil
	}
	return "online banking is not answering", []string{config.StepSite, config.StepAPI}
}

func (a *App) probeBank(ctx context.Context, b *config.Bank) string {
	admin, err := a.adminOf(b.ID)
	if err != nil {
		return "it is not in the setup"
	}
	state, err := admin.state(ctx)
	if err != nil {
		return "its database cannot be reached: " + err.Error()
	}
	for _, id := range ensuredSteps() {
		i := slices.IndexFunc(state.Steps, func(s control.Step) bool { return s.ID == id })
		if i < 0 || !state.Steps[i].Done {
			return "its database is not ready"
		}
		if a.rebuiltSince(b.ID, id, state.Steps[i].CompletedAt) {
			return "its database was rebuilt"
		}
	}
	return ""
}

func (a *App) rebuiltSince(bankID, step string, completed *time.Time) bool {
	a.lifeMu.Lock()
	defer a.lifeMu.Unlock()
	st := a.status[bankID]
	if st == nil || completed == nil {
		return false
	}
	seen := st.Steps[stepIndex(step)].At
	return seen != nil && completed.After(*seen)
}
