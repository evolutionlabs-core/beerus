package core

import (
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/state"
)

func TestTargetChange(t *testing.T) {
	rows := []struct {
		name      string
		bank      *setup.Bank
		resource  *setup.Resource
		suspended bool
		changing  bool
	}{
		{name: "clean", bank: &setup.Bank{ID: "bank-a"}, resource: &setup.Resource{Generation: 3, ObservedGeneration: 3}},
		{name: "suspended in the spec", bank: &setup.Bank{ID: "bank-a", State: setup.StateSuspended}, resource: &setup.Resource{Generation: 3, ObservedGeneration: 3}, suspended: true},
		{name: "change not observed", bank: &setup.Bank{ID: "bank-a"}, resource: &setup.Resource{Generation: 4, ObservedGeneration: 3}, changing: true},
		{name: "suspended and not observed", bank: &setup.Bank{ID: "bank-a", State: setup.StateSuspended}, resource: &setup.Resource{Generation: 4, ObservedGeneration: 3}, suspended: true, changing: true},
		{name: "no resource", bank: &setup.Bank{ID: "bank-a"}},
		{name: "not in the spec", resource: &setup.Resource{Generation: 4, ObservedGeneration: 3}, changing: true},
		{name: "unknown bank"},
	}
	for _, r := range rows {
		s := testStore(t)
		err := s.Update(func(st *setup.Status) (bool, error) {
			if r.bank != nil {
				st.Applied.Spec.Banks = append(st.Applied.Spec.Banks, *r.bank)
			}
			if r.resource != nil {
				x := state.EnsureResource(st, setup.KindBank, "bank-a")
				x.Generation, x.ObservedGeneration = r.resource.Generation, r.resource.ObservedGeneration
			}
			return true, nil
		})
		if err != nil {
			t.Fatalf("%s: %v", r.name, err)
		}
		a := &App{ctl: s}
		suspended, changing, err := a.targetChange("bank-a")
		if r.name == "unknown bank" {
			suspended, changing, err = a.targetChange("bank-z")
		}
		if err != nil || suspended != r.suspended || changing != r.changing {
			t.Errorf("%s: suspended %v, changing %v, %v; want %v, %v", r.name, suspended, changing, err, r.suspended, r.changing)
		}
	}
}
