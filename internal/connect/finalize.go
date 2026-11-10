package connect

import (
	"fmt"
	"slices"

	"github.com/evolutionlabs-core/beerus/internal/setup"
)

type ApplyOptions struct {
	Prune     bool           `json:"prune,omitempty"`
	Money     *setup.Removal `json:"money,omitempty"`
	Confirmed []string       `json:"confirmed,omitempty"`
	SettleNow bool           `json:"settle_now,omitempty"`
}

type finalizer struct {
	name string
	run  func(a *App, r setup.Resource) (*setup.Condition, error)
}

func finalizerNames(list []finalizer) []string {
	out := make([]string, len(list))
	for i, f := range list {
		out[i] = f.name
	}
	return out
}

var bankFinalizers = []finalizer{
	{name: setup.FinalizerStopRouting, run: (*App).stopRouting},
	{name: setup.FinalizerRevokeConnections, run: (*App).revokeBankConnections},
	{name: setup.FinalizerSettleInflight, run: (*App).settleInflight},
	{name: setup.FinalizerDecideMoney, run: (*App).decideMoney},
	{name: setup.FinalizerSettleMoves, run: (*App).settleMoves},
	{name: setup.FinalizerPurgeOwned, run: (*App).purgeBank},
}

var partnerFinalizers = []finalizer{
	{name: setup.FinalizerRevokeConnections, run: (*App).revokePartnerConnections},
	{name: setup.FinalizerPurgeOwned, run: (*App).purgePartner},
}

var finalizerLists = map[string][]finalizer{
	setup.KindBank:    bankFinalizers,
	setup.KindPartner: partnerFinalizers,
}

func nextFinalizer(r setup.Resource) string {
	for _, f := range r.Finalizers {
		if !slices.Contains(r.Done, f) {
			return f
		}
	}
	return ""
}

func findFinalizer(list []finalizer, name string) *finalizer {
	for i := range list {
		if list[i].name == name {
			return &list[i]
		}
	}
	return nil
}

func setCondition(r *setup.Resource, c setup.Condition) bool {
	for i := range r.Conditions {
		cur := r.Conditions[i]
		if cur.Type != c.Type {
			continue
		}
		if cur.Status == c.Status && cur.Reason == c.Reason && cur.Message == c.Message {
			return false
		}
		r.Conditions[i] = c
		return true
	}
	r.Conditions = append(r.Conditions, c)
	return true
}

func clearCondition(r *setup.Resource, kind string) bool {
	n := len(r.Conditions)
	r.Conditions = slices.DeleteFunc(r.Conditions, func(c setup.Condition) bool { return c.Type == kind })
	return len(r.Conditions) != n
}

func (a *App) advanceFinalizers(lists map[string][]finalizer) {
	st, err := a.ctl.status()
	if err != nil {
		a.log.Error("read control.json", "err", err)
		return
	}
	now := a.clock.Real()
	for _, r := range st.Resources {
		if r.Phase != setup.PhaseClosing && r.Phase != setup.PhaseTerminating {
			continue
		}
		if r.NextAttemptAt != nil && now.Before(*r.NextAttemptAt) {
			continue
		}
		name := nextFinalizer(r)
		f := findFinalizer(lists[r.Kind], name)
		if f == nil {
			continue
		}
		var cond *setup.Condition
		var err error
		if f.run != nil {
			cond, err = f.run(a, r)
		}
		kind, id := r.Kind, r.ID
		closed := false
		uerr := a.ctl.update(func(st *setup.Status) (bool, error) {
			cur := findResource(st, kind, id)
			if cur == nil || nextFinalizer(*cur) != name {
				return false, nil
			}
			switch {
			case err != nil:
				cur.Attempts++
				next := now.Add(backoff(cur.Attempts))
				cur.NextAttemptAt = &next
				setCondition(cur, setup.Condition{Type: setup.ConditionDegraded, Status: "True", Reason: setup.ReasonStepFailed, Message: fmt.Sprintf("%s: %v", name, err), ChangedAt: now})
			case cond != nil:
				changed := cur.Attempts != 0 || cur.NextAttemptAt != nil
				cur.Attempts, cur.NextAttemptAt = 0, nil
				cleared := clearCondition(cur, setup.ConditionDegraded)
				return setCondition(cur, *cond) || changed || cleared, nil
			default:
				cur.Done = append(cur.Done, name)
				cur.Attempts, cur.NextAttemptAt = 0, nil
				clearCondition(cur, setup.ConditionDegraded)
				clearCondition(cur, setup.ConditionBlocked)
				if cur.Phase == setup.PhaseClosing && nextFinalizer(*cur) == "" {
					cur.Phase = setup.PhaseClosed
					cur.ObservedGeneration = cur.Generation
					closed = true
					setCondition(cur, setup.Condition{Type: setup.ConditionProgressing, Status: "False", Reason: setup.ReasonClosed, Message: "closed", ChangedAt: now})
				}
			}
			return true, nil
		})
		if uerr != nil {
			a.log.Error("record finalizer", "resource", kind+"/"+id, "finalizer", name, "err", uerr)
			continue
		}
		if closed && kind == setup.KindBank {
			a.lifeMu.Lock()
			if st := a.status[id]; st != nil {
				st.Phase, st.Reason = PhaseClosed, ""
			}
			a.lifeMu.Unlock()
			a.poke()
		}
	}
}
