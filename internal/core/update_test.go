package core

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/state"
)

func TestBankChangeWaitsForNIBSS(t *testing.T) {
	down := true
	var sent []string
	link := control.NewLink("", "")
	link.Client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if down {
			return nil, errors.New("connection refused")
		}
		body, err := io.ReadAll(r.Body)
		sent = append(sent, r.Method+" "+r.URL.Path+" "+string(body))
		return stubReply(http.StatusOK, bankJSON("bank-a", "000011", "active")), err
	})}
	s := testStore(t)
	if err := s.Update(func(st *setup.Status) (bool, error) {
		st.Applied.Spec.Banks = []setup.Bank{{ID: "bank-a", State: setup.StateSuspended}}
		r := state.EnsureResource(st, setup.KindBank, "bank-a")
		r.Phase, r.Generation, r.ObservedGeneration = PhaseActive, 2, 1
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	a := &App{cfg: &config.Config{Banks: []*config.Bank{{ID: "bank-a"}}}, ctl: s, clock: clock.New(), log: slog.New(slog.DiscardHandler), nibssCtl: &nibssAdmin{control.NewNIBSSControl(link, 1)}, status: map[string]*BankStatus{"bank-a": {ID: "bank-a", Phase: PhaseActive}}}
	a.reconcileChanges()
	for _, c := range []struct {
		name, phase string
		down        bool
		observed    int64
		degraded    int
	}{{"NIBSS down", PhaseActive, true, 1, 1}, {"NIBSS confirms", PhaseSuspended, false, 2, 0}} {
		down = c.down
		b, stale := *a.cfg.Bank("bank-a"), a.stale["bank-a"]
		a.finishStale(staleJob{b, stale.generation, stale.version}, a.registerMember(t.Context(), b))
		r, err := a.Resource(setup.KindBank, "bank-a")
		if err != nil || r.ObservedGeneration != c.observed || len(r.Conditions) != c.degraded || r.Phase != c.phase {
			t.Fatalf("%s: observed %d, %d conditions, phase %s, %v", c.name, r.ObservedGeneration, len(r.Conditions), r.Phase, err)
		}
	}
	if len(sent) != 2 || !strings.Contains(sent[1], "/status") || !strings.Contains(sent[1], "suspended") || a.staleMember("bank-a") {
		t.Fatalf("NIBSS was sent %q, bank stale %v", sent, a.staleMember("bank-a"))
	}
}
