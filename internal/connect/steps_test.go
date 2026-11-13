package connect

import (
	"errors"
	"log/slog"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func TestBackoffDoublesToFiveMinutes(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	for i, w := range want {
		if got := backoff(i + 1); got != w {
			t.Errorf("attempt %d: %v, want %v", i+1, got, w)
		}
	}
	if got := backoff(40); got != 5*time.Minute {
		t.Fatalf("attempt 40: %v", got)
	}
}

func TestStepIndexKeepsTheDatabaseThird(t *testing.T) {
	if stepIndex(setup.StepDatabase) != 2 || stepIndex(setup.StepAllocate) != 0 || stepIndex("nope") != -1 {
		t.Fatal("step order changed")
	}
}

func TestDriftRules(t *testing.T) {
	missing := []string{setup.StepDatabase, setup.StepSite}
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
	if err := s.update(func(st *setup.Status) (bool, error) {
		st.Resources = append(st.Resources, r)
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	return &App{ctl: s, clock: clock.New(func() time.Time { return time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC) }), log: slog.New(slog.DiscardHandler)}
}

func TestOneFinalizerPerStep(t *testing.T) {
	a := finalizerApp(t, setup.Resource{Kind: setup.KindBank, ID: "bank-b", Phase: setup.PhaseClosing, Finalizers: []string{"one", "two", "three"}})
	ran := []string{}
	run := func(name string) func(*App, setup.Resource) (*setup.Condition, error) {
		return func(*App, setup.Resource) (*setup.Condition, error) {
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
	st, err := a.ctl.status()
	if err != nil {
		t.Fatal(err)
	}
	if r := findResource(&st, setup.KindBank, "bank-b"); !slices.Equal(r.Done, []string{"one", "two", "three"}) || r.Phase != setup.PhaseClosed || r.ObservedGeneration != r.Generation {
		t.Fatalf("a finalizer without a run did not finish at once, or the close did not complete: %+v", r)
	}
}

func TestFinalizerFailureBacksOff(t *testing.T) {
	a := finalizerApp(t, setup.Resource{Kind: setup.KindBank, ID: "bank-b", Phase: setup.PhaseClosing, Finalizers: []string{"one"}})
	calls := 0
	lists := map[string][]finalizer{setup.KindBank: {{name: "one", run: func(*App, setup.Resource) (*setup.Condition, error) {
		calls++
		return nil, errors.New("postgres is down")
	}}}}
	a.advanceFinalizers(t.Context(), lists)
	a.advanceFinalizers(t.Context(), lists)
	if calls != 1 {
		t.Fatalf("ran %d times inside the backoff", calls)
	}
	st, err := a.ctl.status()
	if err != nil {
		t.Fatal(err)
	}
	r := findResource(&st, setup.KindBank, "bank-b")
	if r.Attempts != 1 || r.NextAttemptAt == nil || len(r.Done) != 0 || len(r.Conditions) != 1 || r.Conditions[0].Reason != setup.ReasonStepFailed {
		t.Fatalf("after a failure: %+v", r)
	}
}

func TestEveryBankTypeHasAHistory(t *testing.T) {
	if !slices.Equal(slices.Sorted(maps.Keys(typeHistoryDays)), setup.BankTypes) {
		t.Fatalf("history days for %v, bank types %v", slices.Sorted(maps.Keys(typeHistoryDays)), setup.BankTypes)
	}
}
