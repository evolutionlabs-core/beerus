package core

import (
	"context"
	"errors"
	"github.com/evolutionlabs-core/beerus/internal/state"
	"log/slog"
	"slices"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func TestDriftRules(t *testing.T) {
	if stepIndex(config.StepDatabase) != 2 || stepIndex(config.StepAllocate) != 0 || stepIndex("nope") != -1 {
		t.Fatal("step order changed")
	}
	missing := []string{config.StepDatabase, config.StepSite}
	cases := []struct {
		phase     string
		done      []string
		redo      []string
		nextPhase string
	}{
		{PhaseActive, nil, missing, PhaseRepairing},
		{PhaseSuspended, nil, missing, PhaseRepairing},
		{PhaseClosing, []string{setup.FinalizerStopRouting}, missing, PhaseClosing},
		{PhaseTerminating, []string{setup.FinalizerSettleMoves}, missing, PhaseTerminating},
		{PhaseTerminating, []string{setup.FinalizerSettleMoves, setup.FinalizerPurgeOwned}, nil, PhaseTerminating},
		{PhaseClosed, nil, nil, PhaseClosed},
	}
	for _, c := range cases {
		redo, phase := driftRepairs(c.phase, c.done, missing)
		if !slices.Equal(redo, c.redo) || phase != c.nextPhase {
			t.Errorf("%s %v: redo %v phase %s", c.phase, c.done, redo, phase)
		}
	}
}

func finalizerApp(t *testing.T, r setup.Resource) *App {
	t.Helper()
	s := testStore(t)
	if err := s.Update(func(st *setup.Status) (bool, error) {
		st.Resources = append(st.Resources, r)
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	return &App{ctl: s, clock: clock.New(), log: slog.New(slog.DiscardHandler)}
}

func TestOneFinalizerPerStep(t *testing.T) {
	a := finalizerApp(t, setup.Resource{Kind: setup.KindBank, ID: "bank-b", Phase: config.PhaseClosing, Finalizers: []string{"one", "two", "three"}})
	ran := []string{}
	run := func(name string) func(*App, context.Context, setup.Resource) (*setup.Condition, error) {
		return func(*App, context.Context, setup.Resource) (*setup.Condition, error) {
			ran = append(ran, name)
			return nil, nil
		}
	}
	lists := map[string][]finalizer{setup.KindBank: {{name: "one", run: run("one")}, {name: "two", run: run("two")}, {name: "three"}}}
	for range 4 {
		a.advanceFinalizers(t.Context(), lists)
	}
	if !slices.Equal(ran, []string{"one", "two"}) {
		t.Fatalf("ran %v", ran)
	}
	st, err := a.ctl.Status()
	if err != nil {
		t.Fatal(err)
	}
	if r := setup.FindResource(&st, setup.KindBank, "bank-b"); !slices.Equal(r.Done, []string{"one", "two", "three"}) || r.Phase != config.PhaseClosed || r.ObservedGeneration != r.Generation {
		t.Fatalf("a finalizer without a run did not finish at once, or the close did not complete: %+v", r)
	}
}

func TestFinalizerFailureBacksOff(t *testing.T) {
	a := finalizerApp(t, setup.Resource{Kind: setup.KindBank, ID: "bank-b", Phase: config.PhaseClosing, Finalizers: []string{"one"}})
	calls := 0
	lists := map[string][]finalizer{setup.KindBank: {{name: "one", run: func(*App, context.Context, setup.Resource) (*setup.Condition, error) {
		calls++
		return nil, errors.New("postgres is down")
	}}}}
	a.advanceFinalizers(t.Context(), lists)
	a.advanceFinalizers(t.Context(), lists)
	if calls != 1 {
		t.Fatalf("ran %d times inside the backoff", calls)
	}
	st, err := a.ctl.Status()
	if err != nil {
		t.Fatal(err)
	}
	r := setup.FindResource(&st, setup.KindBank, "bank-b")
	if r.Attempts != 1 || r.NextAttemptAt == nil || len(r.Done) != 0 || len(r.Conditions) != 1 || r.Conditions[0].Reason != reasonRetrying || r.Conditions[0].Type != setup.ConditionDegraded {
		t.Fatalf("after a failure: %+v", r)
	}
	for range quietAttempts {
		if err := a.ctl.Update(func(st *setup.Status) (bool, error) {
			setup.FindResource(st, setup.KindBank, "bank-b").NextAttemptAt = nil
			return true, nil
		}); err != nil {
			t.Fatal(err)
		}
		a.advanceFinalizers(t.Context(), lists)
	}
	st, err = a.ctl.Status()
	if err != nil {
		t.Fatal(err)
	}
	r = setup.FindResource(&st, setup.KindBank, "bank-b")
	if len(r.Conditions) != 1 || r.Conditions[0].Reason != setup.ReasonStepFailed || r.Conditions[0].Message != "one: postgres is down" {
		t.Fatalf("after repeated failures: %+v", r.Conditions)
	}
}

func TestRecordPhase(t *testing.T) {
	for _, c := range []struct {
		name               string
		phase              string
		gen, observed, rec int64
		wantPhase          string
		wantObserved       int64
	}{
		{"closing is not reopened", PhaseClosing, 3, 2, 3, PhaseClosing, 2},
		{"terminating is not reopened", PhaseTerminating, 3, 2, 3, PhaseTerminating, 2},
		{"closed is not reopened", PhaseClosed, 3, 2, 3, PhaseClosed, 2},
		{"an older generation is observed as it was", PhaseRepairing, 3, 1, 2, PhaseActive, 2},
		{"the current generation is observed", PhaseRepairing, 3, 1, 3, PhaseActive, 3},
	} {
		a := finalizerApp(t, setup.Resource{Kind: setup.KindBank, ID: "bank-b", Phase: c.phase, Generation: c.gen, ObservedGeneration: c.observed})
		a.recordPhase("bank-b", PhaseActive, c.rec)
		st, err := a.ctl.Status()
		if err != nil {
			t.Fatal(err)
		}
		if r := setup.FindResource(&st, setup.KindBank, "bank-b"); r.Phase != c.wantPhase || r.Generation != c.gen || r.ObservedGeneration != c.wantObserved {
			t.Errorf("%s: phase %s, generation %d, observed %d", c.name, r.Phase, r.Generation, r.ObservedGeneration)
		}
	}
}

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
