package connect

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

func (a *App) AddFault(req server.FaultRequest) (server.Fault, error) {
	if a.bankByID(req.Bank) == nil {
		return server.Fault{}, errNotFound
	}
	f, err := server.NewFault(req, a.clock.Now())
	if err != nil {
		return server.Fault{}, err
	}
	f.Bank = req.Bank
	f.ID = server.FaultID(fmt.Sprintf("%s|%s|%d", f.Bank, f.Kind, f.At.UnixNano()))
	a.mu.Lock()
	a.st.Faults = append(a.st.Faults, &f)
	a.dirty()
	a.mu.Unlock()
	a.pokePush()
	return f, nil
}

func (a *App) Faults() []server.Fault {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.faultsLocked(a.clock.Now())
}

func (a *App) faultsLocked(now time.Time) []server.Fault {
	out := []server.Fault{}
	for _, f := range a.st.Faults {
		if f.Live(now) {
			out = append(out, *f)
		}
	}
	return out
}

func (a *App) ClearFaults(bank string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.clock.Now()
	keep := a.st.Faults[:0]
	n := 0
	for _, f := range a.st.Faults {
		switch {
		case !f.Live(now):
		case bank == "" || f.Bank == bank:
			n++
		default:
			keep = append(keep, f)
		}
	}
	a.st.Faults = keep
	a.dirty()
	a.pokePush()
	return n
}

func (a *App) bankDown(id string, at time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.bankDownLocked(id, at)
}

func (a *App) bankDownLocked(id string, at time.Time) bool {
	return slices.ContainsFunc(a.st.Faults, func(f *server.Fault) bool { return f.Kind == server.FaultDown && f.Bank == id && f.Live(at) })
}

func (a *App) bankFaulty(id string, at time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.ContainsFunc(a.st.Faults, func(f *server.Fault) bool { return f.Bank == id && f.Live(at) })
}

func (a *App) takeTimeout(bank string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, f := range a.st.Faults {
		if f.Kind == server.FaultTimeout && f.Bank == bank && f.Left > 0 {
			f.Left--
			a.dirty()
			return f.Outcome
		}
	}
	return ""
}

func (a *App) pruneFaultsLocked(now time.Time) {
	keep := a.st.Faults[:0]
	for _, f := range a.st.Faults {
		if f.Live(now) {
			keep = append(keep, f)
		}
	}
	a.st.Faults = keep
}

func (a *App) ctlFaults(w http.ResponseWriter, r *http.Request) {
	web.WriteJSON(w, http.StatusOK, map[string]any{"data": a.Faults()})
}

func (a *App) ctlAddFault(w http.ResponseWriter, r *http.Request) {
	var req server.FaultRequest
	if err := web.DecodeBody(r, &req); err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", `Send {"kind":"down","bank":"bank-b","for":"1h"} or {"kind":"timeout","bank":"bank-b","outcome":"credited"}.`)
		return
	}
	f, err := a.AddFault(req)
	if err != nil {
		a.bankError(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusCreated, f)
}

func (a *App) ctlClearFaults(w http.ResponseWriter, r *http.Request) {
	bank := r.URL.Query().Get("bank")
	if bank != "" && a.bankByID(bank) == nil {
		web.WriteError(w, r, http.StatusNotFound, "not_found", fmt.Sprintf("No bank %q.", bank))
		return
	}
	web.WriteJSON(w, http.StatusOK, map[string]any{"cleared": a.ClearFaults(bank)})
}

type member struct {
	a  *App
	ls *ledgerSource
}

func (m member) NameEnquiry(ctx context.Context, nuban string) (string, error) {
	if m.a.bankDown(m.ls.Inst.ID, m.a.clock.Now()) {
		return "", fmt.Errorf("%s is down: %w", m.ls.Inst.Name, nip.ErrUnavailable)
	}
	return m.ls.Bank().NameEnquiry(ctx, nuban)
}

func (a *App) bankRefusing(id string) bool {
	return a.bankPhase(id) == PhaseSuspended || a.bankDown(id, a.clock.Now())
}
