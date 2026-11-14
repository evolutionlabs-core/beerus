package connect

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/nip"
)

const (
	FaultDown    = "down"
	FaultTimeout = "timeout"
)

type Fault struct {
	ID      string     `json:"id"`
	Kind    string     `json:"kind"`
	Bank    string     `json:"bank"`
	Outcome string     `json:"outcome,omitempty"`
	Left    int        `json:"left,omitempty"`
	Until   *time.Time `json:"until,omitempty"`
	At      time.Time  `json:"created_at"`
}

func (f *Fault) live(now time.Time) bool {
	switch f.Kind {
	case FaultDown:
		return f.Until == nil || now.Before(*f.Until)
	case FaultTimeout:
		return f.Left > 0
	}
	return false
}

type FaultRequest struct {
	Kind    string `json:"kind"`
	Bank    string `json:"bank"`
	Outcome string `json:"outcome"`
	Count   *int   `json:"count"`
	For     string `json:"for"`
}

const maxFaultFor = 24 * time.Hour

func newFault(req FaultRequest, now time.Time) (Fault, error) {
	f := Fault{Kind: req.Kind, At: now}
	switch req.Kind {
	case FaultDown:
		if req.Outcome != "" || req.Count != nil {
			return Fault{}, invalid("a down fault takes only bank and for")
		}
		if req.For != "" {
			d, err := time.ParseDuration(req.For)
			if err != nil || d <= 0 {
				return Fault{}, invalid("for %q is not a duration above zero, such as 1h", req.For)
			}
			if d > maxFaultFor {
				return Fault{}, invalid("for %q is above the limit of %dh", req.For, int(maxFaultFor.Hours()))
			}
			until := now.Add(d)
			f.Until = &until
		}
	case FaultTimeout:
		if !slices.Contains([]string{nibss.OutcomeCredited, nibss.OutcomeFailed, nibss.OutcomeUnknown}, req.Outcome) {
			return Fault{}, invalid("outcome %q must be credited, failed or unknown", req.Outcome)
		}
		if req.For != "" {
			return Fault{}, invalid("a timeout fault takes a count, not for")
		}
		left := 1
		if req.Count != nil {
			if *req.Count < 1 {
				return Fault{}, invalid("count %d must be 1 or more", *req.Count)
			}
			left = *req.Count
		}
		f.Outcome, f.Left = req.Outcome, left
	default:
		return Fault{}, invalid("kind %q must be down or timeout", req.Kind)
	}
	return f, nil
}

func (a *App) AddFault(req FaultRequest) (Fault, error) {
	if a.bankByID(req.Bank) == nil {
		return Fault{}, errNotFound
	}
	f, err := newFault(req, a.clock.Now())
	if err != nil {
		return Fault{}, err
	}
	f.Bank = req.Bank
	f.ID = faultIDFor(fmt.Sprintf("%s|%s|%d", f.Bank, f.Kind, f.At.UnixNano()))
	a.mu.Lock()
	a.st.Faults = append(a.st.Faults, &f)
	a.dirty()
	a.mu.Unlock()
	a.pokePush()
	return f, nil
}

func (a *App) Faults() []Fault {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.faultsLocked(a.clock.Now())
}

func (a *App) faultsLocked(now time.Time) []Fault {
	out := []Fault{}
	for _, f := range a.st.Faults {
		if f.live(now) {
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
		case !f.live(now):
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
	return slices.ContainsFunc(a.st.Faults, func(f *Fault) bool { return f.Kind == FaultDown && f.Bank == id && f.live(at) })
}

func (a *App) bankFaulty(id string, at time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.ContainsFunc(a.st.Faults, func(f *Fault) bool { return f.Bank == id && f.live(at) })
}

func (a *App) takeTimeout(bank string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, f := range a.st.Faults {
		if f.Kind == FaultTimeout && f.Bank == bank && f.Left > 0 {
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
		if f.live(now) {
			keep = append(keep, f)
		}
	}
	a.st.Faults = keep
}

func (a *App) ctlFaults(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"data": a.Faults()})
}

func (a *App) ctlAddFault(w http.ResponseWriter, r *http.Request) {
	var req FaultRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", `Send {"kind":"down","bank":"bank-b","for":"1h"} or {"kind":"timeout","bank":"bank-b","outcome":"credited"}.`)
		return
	}
	f, err := a.AddFault(req)
	if err != nil {
		a.bankError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, f)
}

func (a *App) ctlClearFaults(w http.ResponseWriter, r *http.Request) {
	bank := r.URL.Query().Get("bank")
	if bank != "" && a.bankByID(bank) == nil {
		writeError(w, r, http.StatusNotFound, "not_found", fmt.Sprintf("No bank %q.", bank))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cleared": a.ClearFaults(bank)})
}

type member struct {
	a  *App
	ls *ledgerSource
}

func (m member) NameEnquiry(ctx context.Context, nuban string) (string, error) {
	if m.a.bankDown(m.ls.inst.ID, m.a.clock.Now()) {
		return "", fmt.Errorf("%s is down: %w", m.ls.inst.Name, nip.ErrUnavailable)
	}
	return m.ls.Bank().NameEnquiry(ctx, nuban)
}
