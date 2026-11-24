package core

import (
	"errors"
	"net"
	"strconv"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/bank/participant"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/state"
)

func startCore(t *testing.T, core *App) string {
	t.Helper()
	ln := loopback(t, "127.0.0.1:0")
	core.cfg.Connect.ControlPort = ln.Addr().(*net.TCPAddr).Port
	return serve(t, ln, core.ControlHandler())
}

func bankOn(t *testing.T, core *App, coreURL, id string) *participant.Participant {
	t.Helper()
	p, err := participant.New(t.Context(), core.cfg.Dir, participant.Options{Logger: core.log, BankID: id, CoreURL: coreURL, Bind: "127.0.0.1", DatabaseURL: core.cfg.DatabaseURL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func setBankPhase(a *App, id, phase string) {
	a.lifeMu.Lock()
	defer a.lifeMu.Unlock()
	a.status[id].Phase = phase
}

func TestBanks(t *testing.T) {
	core := newTestApp(t)
	coreURL := startCore(t, core)
	t.Run("retire", func(t *testing.T) { retireRows(t, core, coreURL) })
	t.Run("verify", func(t *testing.T) { verifyRows(t, core) })
	v, err := participantValidator()
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	t.Run("participant control contract", func(t *testing.T) { controlContract(t, v) })
}

func retireRows(t *testing.T, a *App, coreURL string) {
	b := a.cfg.Bank("bank-a")
	if a.bankRetired(b.ID) {
		t.Fatal("a bank counted as retired before it was")
	}
	setBankPhase(a, b.ID, PhaseTerminating)
	if err := a.retireBank(t.Context(), b); err == nil || !control.Unreachable(err) {
		t.Fatalf("an unreachable live bank was retired: %v", err)
	}
	if w, err := a.wishFor(b.ID); !a.bankRetired(b.ID) || err != nil || w.phase != PhaseClosed {
		t.Fatalf("the retirement was not saved: %+v, %v", w, err)
	}
	setBankPhase(a, b.ID, PhaseClosed)
	if err := a.retireBank(t.Context(), b); err != nil {
		t.Fatalf("a closed bank that is not running: %v", err)
	}
	setBankPhase(a, b.ID, PhaseTerminating)
	if err := a.ctl.Update(func(st *setup.Status) (bool, error) {
		delete(setup.FindResource(st, setup.KindBank, b.ID).Steps, stepRetired)
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	p := bankOn(t, a, coreURL, b.ID)
	serve(t, loopback(t, "127.0.0.1:"+strconv.Itoa(b.Port)), p.Server().API())
	if err := a.retireBank(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	if phase, _ := p.Server().Phase(); phase != PhaseClosed {
		t.Fatalf("the bank is %s after it was retired", phase)
	}
}

func verifyRows(t *testing.T, a *App) {
	b := a.cfg.Bank("bank-b")
	a.lifeMu.Lock()
	a.status[b.ID].Steps[stepIndex(config.StepOpenBankingSchema)].Done = false
	a.lifeMu.Unlock()
	for _, r := range []struct {
		name, phase, want string
		retire            bool
	}{
		{"not ready", PhaseActive, "Bank B is not ready, so its books cannot be checked. Try again soon.", false},
		{"closed", PhaseClosed, "", false},
		{"retired", PhaseTerminating, "", true},
	} {
		setBankPhase(a, b.ID, r.phase)
		if r.retire {
			a.recordBank(b.ID, func(res *setup.Resource) bool {
				res.Steps[stepRetired] = state.StepDone
				return true
			})
		}
		a.mu.Lock()
		saved := a.cfg.Banks
		a.cfg.Banks = []*config.Bank{b}
		a.mu.Unlock()
		_, err := a.VerifyBank(t.Context())
		a.mu.Lock()
		a.cfg.Banks = saved
		a.mu.Unlock()
		got := ""
		if err != nil {
			got = err.Error()
		}
		var notReady *bankNotReadyError
		if got != r.want || (r.want != "" && !errors.As(err, &notReady)) {
			t.Errorf("%s: got %q, want %q", r.name, got, r.want)
		}
	}
}
