package connect

import (
	"errors"
	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/bank/participant"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/state"
)

func coreOf(t *testing.T) *App {
	t.Helper()
	fixed := Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	a, _ := newTestAppWithNIBSS(t, fixed)
	a.role = RoleCore
	return a
}

func setBankPhase(a *App, id, phase string) {
	a.lifeMu.Lock()
	defer a.lifeMu.Unlock()
	a.status[id].Phase = phase
}

func TestRetiringABankNeedsItsAcknowledgement(t *testing.T) {
	a := coreOf(t)
	b := a.cfg.Bank("bank-a")
	ctx := t.Context()
	if a.bankRetired(b.ID) {
		t.Fatal("a bank counted as retired before it was")
	}
	setBankPhase(a, b.ID, PhaseTerminating)
	err := a.retireBank(ctx, b)
	if err == nil || !server.Unreachable(err) {
		t.Fatalf("an unreachable live bank was retired: %v", err)
	}
	if !a.bankRetired(b.ID) {
		t.Fatal("the retirement was not saved")
	}
	w, err := a.wishFor(b.ID)
	if err != nil || w.phase != PhaseClosed {
		t.Fatalf("wish after retiring: %+v, %v", w, err)
	}
	setBankPhase(a, b.ID, PhaseClosed)
	if err := a.retireBank(ctx, b); err != nil {
		t.Fatalf("a closed bank that is not running: %v", err)
	}
	setBankPhase(a, b.ID, PhaseTerminating)
	if err := a.ctl.Update(func(st *setup.Status) (bool, error) {
		delete(setup.FindResource(st, setup.KindBank, b.ID).Steps, stepRetired)
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	p, err := participant.New(ctx, a.cfg.Dir, participant.Options{Logger: a.log, BankID: b.ID, CoreURL: startCore(t, a), Bind: "127.0.0.1", DatabaseURL: a.cfg.DatabaseURL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(b.Port))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: p.Server().API(), ReadHeaderTimeout: probeLimit}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ln)
	}()
	t.Cleanup(func() {
		_ = srv.Close()
		<-done
	})
	if err := a.retireBank(ctx, b); err != nil {
		t.Fatal(err)
	}
	if phase, _ := p.Server().Phase(); phase != PhaseClosed {
		t.Fatalf("the bank is %s after it was retired", phase)
	}
}

func TestVerifyBankNamesABankThatIsNotReady(t *testing.T) {
	a := coreOf(t)
	b := a.cfg.Bank("bank-a")
	a.lifeMu.Lock()
	a.status[b.ID].Steps[stepIndex(config.StepOpenBankingSchema)].Done = false
	a.lifeMu.Unlock()
	rows := []struct {
		name   string
		phase  string
		retire bool
		want   string
	}{
		{"not ready", PhaseActive, false, "Bank A is not ready, so its books cannot be checked. Try again soon."},
		{"closed", PhaseClosed, false, ""},
		{"retired", PhaseTerminating, true, ""},
	}
	for _, r := range rows {
		setBankPhase(a, b.ID, r.phase)
		if r.retire {
			a.recordBank(b.ID, func(res *setup.Resource) bool {
				res.Steps[stepRetired] = state.StepDone
				return true
			})
		}
		a.mu.Lock()
		only := []*config.Bank{b}
		saved := a.cfg.Banks
		a.cfg.Banks = only
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
