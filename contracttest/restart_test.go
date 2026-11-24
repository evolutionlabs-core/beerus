package contracttest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/aggregator"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/core"
	"github.com/evolutionlabs-core/beerus/internal/install"
	"github.com/evolutionlabs-core/beerus/internal/postgres"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

var createSteps = []string{config.StepAllocate, config.StepDirectory, config.StepDatabase, config.StepLedgerSchema, config.StepOpenBankingSchema, config.StepFiles, config.StepCBN, config.StepSite, config.StepAPI}

func storedStatus(t *testing.T, dir string) setup.Status {
	t.Helper()
	st, err := setup.LoadStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func storedBank(t *testing.T, dir, id string) setup.Resource {
	t.Helper()
	for _, r := range storedStatus(t, dir).Resources {
		if r.Kind == setup.KindBank && r.ID == id {
			return r
		}
	}
	t.Fatalf("no Bank/%s in control.json", id)
	return setup.Resource{}
}

func activeBank(t *testing.T, dir, id string) setup.Resource {
	t.Helper()
	waitFor(t, waitLimit, "bank "+id+" to be stored as active", func() bool { return storedBank(t, dir, id).Phase == config.PhaseActive })
	return storedBank(t, dir, id)
}

func checkProvisioned(t *testing.T, r setup.Resource) {
	t.Helper()
	if r.Phase != config.PhaseActive || r.ObservedGeneration != r.Generation || r.Attempts != 0 || r.NextAttemptAt != nil {
		t.Errorf("%s: phase %s generation %d/%d attempts %d next %v", r.ID, r.Phase, r.ObservedGeneration, r.Generation, r.Attempts, r.NextAttemptAt)
	}
	for _, step := range createSteps {
		if r.Steps[step] != "done" {
			t.Errorf("%s: step %s is %q", r.ID, step, r.Steps[step])
		}
	}
}

func blockedOn(app *core.App, id, reason, text string) bool {
	r, err := app.Resource(setup.KindBank, id)
	if err != nil {
		return false
	}
	for _, c := range r.Conditions {
		if c.Type == setup.ConditionBlocked && c.Status == "True" && c.Reason == reason && strings.Contains(c.Message, text) {
			return true
		}
	}
	return false
}

type run struct {
	app  *core.App
	stop func()
}

func runOnce(t *testing.T, cfg *config.Config) run {
	t.Helper()
	ns, err := startNodes(cfg, aggregator.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			if err := ns.halt(); err != nil {
				t.Errorf("stop the stack: %v", err)
			}
		})
	}
	t.Cleanup(stop)
	return run{app: ns.app, stop: stop}
}

func TestRestarts(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a full stack; run without -short")
	}
	if os.Getenv("BEERUS_CONTRACT_DIR") != "" {
		t.Skip("needs an in-process stack")
	}
	cfg, err := initStack(t.TempDir(), "http://127.0.0.1:1/webhooks")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := install.Reset(context.WithoutCancel(t.Context()), cfg.Dir, cfg.DatabaseURL); err != nil && !errors.Is(err, install.ErrWrongName) {
			t.Error(err)
		}
	})
	token, err := config.ReadSecret(cfg.Path("control-token"))
	if err != nil {
		t.Fatal(err)
	}
	s := &Stack{t: t, flow: "restarts", env: &env{cfg: cfg, ctlToken: token, people: map[string]persona{}}}
	start := func() run {
		t.Helper()
		loaded, err := setup.LoadConfig(cfg.Dir)
		if err != nil {
			t.Fatal(err)
		}
		return runOnce(t, loaded)
	}

	r := start()
	waitFor(t, waitLimit, "both banks to be active", func() bool {
		return !slices.ContainsFunc(r.app.BankStatuses(), func(b core.BankStatus) bool { return b.Phase != core.PhaseActive })
	})
	for _, id := range []string{"bank-a", "bank-b"} {
		checkProvisioned(t, activeBank(t, cfg.Dir, id))
	}
	from, to := s.persona("tunde").Accounts[0], s.persona("chidi").Accounts[0]
	if tr, err := s.payTry("bank-a", from, "bank-b", to, "40000", nil); err != nil || tr["status"] != "success" {
		t.Fatalf("transfer: %+v %v", tr, err)
	}
	if _, err := r.app.PurgeBank(t.Context(), "bank-b", false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, waitLimit, "settle-inflight to wait", func() bool {
		return blockedOn(r.app, "bank-b", setup.ReasonSettlementPending, "to settle 1 transfer (net ₦40,000.00)")
	})
	if got := storedBank(t, cfg.Dir, "bank-b").Done; !slices.Equal(got, []string{setup.FinalizerStopRouting, setup.FinalizerRevokeConnections}) {
		t.Fatalf("finalizers done before the first restart: %v", got)
	}
	r.stop()

	r = start()
	waitFor(t, waitLimit, "the removal to resume and wait for settlement", func() bool {
		return blockedOn(r.app, "bank-b", setup.ReasonSettlementPending, "to settle 1 transfer")
	})
	if _, err := r.app.SettleNow(t.Context()); err != nil {
		t.Fatal(err)
	}
	const decision = "holds ₦125,000.00 for 1 customer"
	waitFor(t, waitLimit, "the money decision", func() bool {
		return blockedOn(r.app, "bank-b", setup.ReasonMoneyDecisionNeeded, decision)
	})
	r.stop()

	r = start()
	waitFor(t, waitLimit, "the money decision after the second restart", func() bool {
		return blockedOn(r.app, "bank-b", setup.ReasonMoneyDecisionNeeded, decision)
	})
	spec := storedStatus(t, cfg.Dir).Applied
	spec.Spec.Banks = slices.DeleteFunc(spec.Spec.Banks, func(b setup.Bank) bool { return b.ID == "bank-b" })
	if _, err := r.app.Apply(t.Context(), spec, core.ApplyOptions{Prune: true, Money: &setup.Removal{Money: setup.MoneyTransfer, To: "bank-a"}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, waitLimit, "the moves to wait for settlement", func() bool {
		return blockedOn(r.app, "bank-b", setup.ReasonSettlementPending, "to settle 1 transfer (net ₦125,000.00)")
	})
	if got := storedBank(t, cfg.Dir, "bank-b").Done; !slices.Equal(got, []string{setup.FinalizerStopRouting, setup.FinalizerRevokeConnections, setup.FinalizerSettleInflight, setup.FinalizerDecideMoney}) {
		t.Fatalf("finalizers done before the third restart: %v", got)
	}
	r.stop()

	r = start()
	waitFor(t, waitLimit, "the moves to wait for settlement after the third restart", func() bool {
		return blockedOn(r.app, "bank-b", setup.ReasonSettlementPending, "to settle 1 transfer (net ₦125,000.00)")
	})
	if _, err := r.app.SettleNow(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, waitLimit, "bank-b to be removed", func() bool {
		_, err := r.app.Resource(setup.KindBank, "bank-b")
		return err != nil
	})
	if ck, err := r.app.CheckSettlement(t.Context()); err != nil || len(ck.Problems) != 0 {
		t.Fatalf("settlement after the restarts: %+v %v", ck, err)
	}
	if ck, err := r.app.VerifyBank(t.Context()); err != nil || !ck.OK() {
		t.Fatalf("bank ledgers after the restarts: %v %v", err, ck)
	}
	r.stop()

	st := storedStatus(t, cfg.Dir)
	later := time.Now().Add(time.Hour)
	for i := range st.Resources {
		if res := &st.Resources[i]; res.Kind == setup.KindBank && res.ID == "bank-a" {
			res.Phase = config.PhaseProvisioning
			res.Steps = map[string]string{config.StepAllocate: "done", config.StepDirectory: "done"}
			res.Attempts, res.NextAttemptAt = 3, &later
		}
	}
	if err := setup.SaveStatus(cfg.Dir, st); err != nil {
		t.Fatal(err)
	}
	r = start()
	waitFor(t, waitLimit, "bank-a to be provisioned again", func() bool {
		return !slices.ContainsFunc(r.app.BankStatuses(), func(b core.BankStatus) bool { return b.Phase != core.PhaseActive })
	})
	checkProvisioned(t, activeBank(t, cfg.Dir, "bank-a"))
	r.stop()

	c := newCLIAt(t, cfg.Dir)
	if err := os.WriteFile(cfg.Path("running"), []byte("now"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := c.fails("reset", storedStatus(t, cfg.Dir).Setup); !strings.Contains(got, "Beerus is running") {
		t.Errorf("reset while running: %q", got)
	}
	if err := os.Remove(cfg.Path("running")); err != nil {
		t.Fatal(err)
	}
	if got := c.fails("reset", "wrong"); !strings.Contains(got, fmt.Sprintf("holds setup %q, not %q", storedStatus(t, cfg.Dir).Setup, "wrong")) {
		t.Errorf("reset with the wrong name: %q", got)
	}
	var dropped []any
	dropped, _ = c.json("reset", storedStatus(t, cfg.Dir).Setup)["dropped"].([]any)
	for _, id := range []string{"bank-a", setup.SystemCBN, setup.SystemNIBSS} {
		if !slices.Contains(dropped, any(config.DatabaseName(cfg.Dir, id))) {
			t.Errorf("%s was not dropped: %v", id, dropped)
		}
	}
	prefix := strings.TrimSuffix(config.DatabaseName(cfg.Dir, "x"), "x")
	if left, err := postgres.List(t.Context(), cfg.DatabaseURL, prefix); err != nil || len(left) != 0 {
		t.Errorf("databases left after reset: %v %v", left, err)
	}
	if entries, err := os.ReadDir(cfg.Dir); err != nil || len(entries) != 1 || entries[0].Name() != setup.LocalFile {
		t.Errorf("left in the dir: %v %v", entries, err)
	}
}
