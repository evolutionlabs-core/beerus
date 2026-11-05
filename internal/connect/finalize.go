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

var bankFinalizers = []finalizer{
	{name: setup.FinalizerStopRouting},
	{name: setup.FinalizerRevokeConnections},
	{name: setup.FinalizerSettleInflight},
	{name: setup.FinalizerDecideMoney},
	{name: setup.FinalizerSettleMoves},
	{name: setup.FinalizerPurgeOwned},
}

var partnerFinalizers = []finalizer{
	{name: setup.FinalizerRevokeConnections},
	{name: setup.FinalizerPurgeOwned},
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

func setCondition(r *setup.Resource, c setup.Condition) {
	for i := range r.Conditions {
		if r.Conditions[i].Type == c.Type {
			r.Conditions[i] = c
			return
		}
	}
	r.Conditions = append(r.Conditions, c)
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
		if f == nil || f.run == nil {
			continue
		}
		cond, err := f.run(a, r)
		kind, id := r.Kind, r.ID
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
				setCondition(cur, *cond)
			default:
				cur.Done = append(cur.Done, name)
				cur.Attempts, cur.NextAttemptAt = 0, nil
			}
			return true, nil
		})
		if uerr != nil {
			a.log.Error("record finalizer", "resource", kind+"/"+id, "finalizer", name, "err", uerr)
		}
	}
}
