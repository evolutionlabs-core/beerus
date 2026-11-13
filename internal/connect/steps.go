package connect

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

const (
	backoffFirst = time.Second
	backoffMax   = 5 * time.Minute
)

var bankSteps = []struct{ id, name string }{
	{setup.StepAllocate, "Allocating codes, port and database"},
	{setup.StepDirectory, "Registering with NIBSS"},
	{setup.StepDatabase, "Creating the bank database"},
	{setup.StepLedgerSchema, "Migrating bank ledger"},
	{setup.StepOpenBankingSchema, "Migrating open banking"},
	{setup.StepCerts, "Issuing certificates"},
	{setup.StepCBN, "Opening the CBN settlement account"},
	{setup.StepSite, "Starting online banking"},
	{setup.StepAPI, "Starting the open banking API"},
}

func stepIndex(id string) int {
	return slices.IndexFunc(bankSteps, func(s struct{ id, name string }) bool { return s.id == id })
}

func backoff(attempts int) time.Duration {
	d := backoffFirst
	for i := 1; i < attempts && d < backoffMax; i++ {
		d *= 2
	}
	return min(d, backoffMax)
}

func newStatus(b *config.Bank, done bool) *BankStatus {
	st := &BankStatus{ID: b.ID, Name: b.Name, Phase: PhaseProvisioning, URL: b.URL()}
	for _, s := range bankSteps {
		st.Steps = append(st.Steps, BankStep{ID: s.id, Name: s.name, Done: done})
	}
	if done {
		st.Phase = runningPhase(b)
	}
	if b.Closed() {
		st.Phase = PhaseClosed
	}
	return st
}

func runningPhase(b *config.Bank) string {
	if b.Suspended() {
		return PhaseSuspended
	}
	return PhaseActive
}

func (a *App) initLifecycle(unready map[string]bool) []*config.Bank {
	a.status = map[string]*BankStatus{}
	a.sites = map[string]*siteServer{}
	a.strikes = map[string]int{}
	stored, err := a.ctl.status()
	if err != nil {
		a.log.Error("read control.json", "err", err)
	}
	var resume []*config.Bank
	for _, b := range a.cfg.Banks {
		st := newStatus(b, a.ledgerFor(b.ID) != nil)
		a.status[b.ID] = st
		r := findResource(&stored, setup.KindBank, b.ID)
		if r != nil && (r.Phase == PhaseClosing || r.Phase == PhaseTerminating) {
			st.Phase = r.Phase
			continue
		}
		if b.Closed() {
			continue
		}
		if !unready[b.ID] && (r == nil || (r.Phase != PhaseProvisioning && r.Phase != PhaseRepairing)) {
			continue
		}
		st.Phase = PhaseProvisioning
		if r != nil && r.Phase == PhaseRepairing {
			st.Phase = PhaseRepairing
		}
		for i := range st.Steps {
			st.Steps[i].Done = st.Steps[i].ID == setup.StepAllocate && r != nil && r.Steps[setup.StepAllocate] == stepDone
		}
		resume = append(resume, b)
	}
	return resume
}

func (a *App) recordBank(id string, fn func(r *setup.Resource) bool) {
	err := a.ctl.update(func(st *setup.Status) (bool, error) {
		r := findResource(st, setup.KindBank, id)
		if r == nil {
			return false, nil
		}
		return fn(r), nil
	})
	if err != nil {
		a.log.Error("record bank status", "bank", id, "err", err)
	}
}

func (a *App) recordPhase(id, phase string) {
	a.recordBank(id, func(r *setup.Resource) bool {
		changed := r.Phase != phase
		r.Phase = phase
		if phase == PhaseActive || phase == PhaseSuspended {
			changed = changed || r.ObservedGeneration != r.Generation || r.Attempts != 0 || r.NextAttemptAt != nil
			r.ObservedGeneration, r.Attempts, r.NextAttemptAt = r.Generation, 0, nil
			changed = clearCondition(r, setup.ConditionDegraded) || changed
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
		next := now.Add(backoff(st.Attempts))
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
		if r.Steps[id] == stepDone {
			return false
		}
		r.Steps[id] = stepDone
		return true
	})
	return err == nil
}

func (a *App) reconcileBanks() {
	a.checkDrift()
	now := a.clock.Real()
	stored, err := a.ctl.status()
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
		a.provision(b)
	}
	a.reconcileChanges()
	a.advanceFinalizers(finalizerLists)
}

func (a *App) needsRepairLocked(st *BankStatus, stored setup.Status) bool {
	if st.Phase != PhaseClosing && st.Phase != PhaseTerminating {
		return false
	}
	r := findResource(&stored, setup.KindBank, st.ID)
	if r == nil || nextFinalizer(*r) == setup.FinalizerPurgeOwned || slices.Contains(r.Done, setup.FinalizerPurgeOwned) {
		return false
	}
	return slices.ContainsFunc(st.Steps, func(s BankStep) bool { return !s.Done })
}

func (a *App) provision(b *config.Bank) {
	a.lifeMu.Lock()
	st := a.status[b.ID]
	a.lifeMu.Unlock()
	ok := a.step(st, setup.StepAllocate, func() error {
		return a.claimBank(b)
	}) && a.step(st, setup.StepDirectory, func() error {
		return a.registerMember(b)
	}) && a.step(st, setup.StepDatabase, func() error {
		return store.Create(context.Background(), a.cfg.DatabaseURL, b.Database)
	}) && a.step(st, setup.StepLedgerSchema, func() error {
		n, err := store.Migrate(context.Background(), a.cfg.DatabaseURL, b.Database, ledger.Migrate)
		if err != nil {
			return err
		}
		a.nameStep(st, setup.StepLedgerSchema, n)
		return nil
	}) && a.step(st, setup.StepOpenBankingSchema, func() error {
		n, err := store.Migrate(context.Background(), a.cfg.DatabaseURL, b.Database, openbanking.Migrate)
		if err != nil {
			return err
		}
		a.nameStep(st, setup.StepOpenBankingSchema, n)
		return a.openLedger(b)
	}) && a.step(st, setup.StepCerts, func() error {
		return a.bankCerts(b)
	}) && a.step(st, setup.StepCBN, func() error {
		return a.cbnMember(b)
	}) && a.step(st, setup.StepSite, func() error {
		if b.Closed() {
			return nil
		}
		if err := a.startBankSite(b); err != nil {
			return err
		}
		return a.probeSite(b)
	}) && a.step(st, setup.StepAPI, func() error {
		if b.Closed() {
			return nil
		}
		return a.checkBankAPI(b)
	})
	a.lifeMu.Lock()
	leaving := st.Phase == PhaseClosing || st.Phase == PhaseTerminating
	a.lifeMu.Unlock()
	if !ok || leaving {
		return
	}
	phase := runningPhase(b)
	a.recordPhase(b.ID, phase)
	a.lifeMu.Lock()
	st.Phase, st.Reason, st.Attempts, st.NextAttemptAt = phase, "", 0, nil
	a.lifeMu.Unlock()
}

func (a *App) nameStep(st *BankStatus, id string, migrations int) {
	i := stepIndex(id)
	a.lifeMu.Lock()
	defer a.lifeMu.Unlock()
	st.Steps[i].Name = bankSteps[i].name + " (" + migrationCount(migrations) + ")"
}

func migrationCount(n int) string {
	switch n {
	case 0:
		return "no new migrations"
	case 1:
		return "1 migration"
	}
	return fmt.Sprintf("%d migrations", n)
}

func (a *App) openLedger(b *config.Bank) error {
	a.mu.Lock()
	ls := a.ledgerFor(b.ID)
	a.mu.Unlock()
	if ls == nil {
		var err error
		if ls, err = openLedgerSource(context.Background(), a.cfg, a.ctl, b); err != nil {
			return err
		}
		a.mu.Lock()
		a.addLedger(ls)
		a.mu.Unlock()
	}
	a.attachMember(ls)
	return nil
}

func (a *App) bankCerts(b *config.Bank) error {
	c := config.Config{Dir: a.cfg.Dir, Banks: []*config.Bank{b}}
	if err := c.EnsureBankCertificates(nil, a.clock.Real()); err != nil {
		return fmt.Errorf("issue certificates for %s: %w", b.ID, err)
	}
	return a.ctl.Claim(setup.KindBank, b.ID, setup.Owned{Kind: ownedDir, Name: "banks/" + b.ID})
}

func (a *App) cbnMember(b *config.Bank) error {
	err := a.ensureRTGS()
	a.mu.Lock()
	r := a.rtgs
	a.mu.Unlock()
	if err != nil {
		return err
	}
	if r == nil {
		return errors.New("the CBN ledger is not open yet")
	}
	if err := r.member(b.ID, bank.GenesisSettlement, bank.GenesisVault); err != nil {
		return fmt.Errorf("open the CBN settlement account of %s: %w", b.ID, err)
	}
	return nil
}

func (a *App) probeSite(b *config.Bank) error {
	a.lifeMu.Lock()
	serving := a.serving
	a.lifeMu.Unlock()
	if !serving || a.role == RoleCore {
		return nil
	}
	resp, err := a.probeHTTP.Get(a.siteProbeURL(b))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK && !(b.Suspended() && resp.StatusCode == http.StatusServiceUnavailable) {
		return fmt.Errorf("online banking answered %d", resp.StatusCode)
	}
	return nil
}

func (a *App) checkBankAPI(b *config.Bank) error {
	a.lifeMu.Lock()
	serving := a.serving
	site := a.sites[b.ID]
	a.lifeMu.Unlock()
	if !serving || a.role == RoleCore {
		return nil
	}
	a.obMu.Lock()
	ob := a.obs[b.ID]
	a.obMu.Unlock()
	if site == nil || site.api == nil || ob == nil {
		return fmt.Errorf("the open banking API of %s is not running", b.ID)
	}
	return nil
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
	stored, err := a.ctl.status()
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
		if r := findResource(&stored, setup.KindBank, b.ID); r != nil {
			done = r.Done
		}
		a.lifeMu.Lock()
		st := a.status[b.ID]
		redo, phase := driftRepairs(st.Phase, done, missing)
		a.lifeMu.Unlock()
		if len(redo) == 0 {
			continue
		}
		a.detach(b, redo[0])
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
	a.mu.Lock()
	ls := a.ledgerFor(b.ID)
	a.mu.Unlock()
	rebuild := []string{setup.StepDatabase, setup.StepLedgerSchema, setup.StepOpenBankingSchema, setup.StepSite, setup.StepAPI}
	if ls == nil {
		return "its ledger is not open", rebuild
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ls.Ping(ctx); err != nil {
		return "its database cannot be reached: " + err.Error(), rebuild
	}
	if b.Closed() {
		return "", nil
	}
	resp, err := a.probeHTTP.Get(a.siteProbeURL(b))
	if err == nil {
		resp.Body.Close()
		return "", nil
	}
	return "online banking is not answering", []string{setup.StepSite, setup.StepAPI}
}

func (a *App) detach(b *config.Bank, from string) {
	if from == setup.StepDatabase {
		a.mu.Lock()
		var ls *ledgerSource
		for i, l := range a.ledgers {
			if l.inst.ID == b.ID {
				ls = l
				a.ledgers = append(a.ledgers[:i:i], a.ledgers[i+1:]...)
				a.poolMu.Lock()
				delete(a.pools, b.ID)
				a.poolMu.Unlock()
				break
			}
		}
		s := a.nibss
		a.mu.Unlock()
		if s != nil {
			s.Attach(b.ID, nil)
		}
		if ls != nil {
			ls.Close()
		}
		return
	}
	a.lifeMu.Lock()
	site := a.sites[b.ID]
	delete(a.sites, b.ID)
	a.lifeMu.Unlock()
	if site != nil {
		_ = site.srv.Close()
		if site.api != nil {
			_ = site.api.Close()
		}
	}
}
