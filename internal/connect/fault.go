package connect

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

type remoteFaults struct {
	Data       []server.FaultView `json:"data"`
	NextCursor string             `json:"next_cursor"`
}

func (a *App) faultSet(id string) *server.Faults {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.faults == nil {
		a.faults = map[string]*server.Faults{}
	}
	if a.faults[id] == nil {
		a.faults[id] = &server.Faults{}
	}
	return a.faults[id]
}

func (a *App) AddFault(ctx context.Context, req server.FaultRequest) (server.Fault, error) {
	b, ok := a.bankCopy(req.Bank)
	if !ok {
		return server.Fault{}, errNotFound
	}
	f, err := server.NewFault(req, a.clock.Now())
	if err != nil {
		return server.Fault{}, err
	}
	f.Bank = req.Bank
	if a.role != RoleCore {
		f.ID = server.FaultID(web.NewID("flt"))
		a.faultSet(b.ID).Add(f, a.clock.Now())
		return f, nil
	}
	req.Bank = ""
	var view server.FaultView
	if _, err := a.link.Request(ctx, b.URL(), http.MethodPost, "/v1/faults", http.Header{"Idempotency-Key": {web.NewID("flt")}}, req, &view); err != nil {
		return server.Fault{}, fmt.Errorf("add a fault at %s: %w", b.ID, err)
	}
	return faultOf(b.ID, view), nil
}

func faultOf(bank string, v server.FaultView) server.Fault {
	return server.Fault{ID: v.ID, Kind: v.Kind, Bank: bank, Outcome: v.Outcome, Left: v.Left, Until: v.UntilAt, At: v.CreatedAt}
}

func (a *App) Faults(ctx context.Context) ([]server.Fault, error) {
	out := []server.Fault{}
	var errs []error
	for _, b := range a.bankList() {
		if a.role != RoleCore {
			for _, f := range a.faultSet(b.ID).Live(a.clock.Now()) {
				f.Bank = b.ID
				out = append(out, f)
			}
			continue
		}
		if !pushable(a.bankPhase(b.ID)) {
			continue
		}
		list, err := a.bankFaults(ctx, b)
		if err != nil {
			errs = append(errs, fmt.Errorf("list faults of %s: %w", b.ID, err))
			continue
		}
		out = append(out, list...)
	}
	return out, errors.Join(errs...)
}

func (a *App) bankFaults(ctx context.Context, b *config.Bank) ([]server.Fault, error) {
	out := []server.Fault{}
	cursor := ""
	for {
		path := "/v1/faults?limit=" + strconv.Itoa(server.FaultPageMax)
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		var page remoteFaults
		if _, err := a.link.Request(ctx, b.URL(), http.MethodGet, path, nil, nil, &page); err != nil {
			return nil, err
		}
		for _, v := range page.Data {
			out = append(out, faultOf(b.ID, v))
		}
		if page.NextCursor == "" {
			return out, nil
		}
		cursor = page.NextCursor
	}
}

func (a *App) ClearFaults(ctx context.Context, bank string) (int, error) {
	n := 0
	var errs []error
	for _, b := range a.bankList() {
		if bank != "" && b.ID != bank {
			continue
		}
		if a.role != RoleCore {
			n += a.faultSet(b.ID).Clear(a.clock.Now())
			continue
		}
		if !pushable(a.bankPhase(b.ID)) {
			continue
		}
		var out struct {
			Cleared int `json:"cleared"`
		}
		if _, err := a.link.Request(ctx, b.URL(), http.MethodDelete, "/v1/faults", nil, nil, &out); err != nil {
			errs = append(errs, fmt.Errorf("clear faults of %s: %w", b.ID, err))
			continue
		}
		n += out.Cleared
	}
	return n, errors.Join(errs...)
}

func (a *App) faultBanks(ctx context.Context, kind string) map[string]bool {
	out := map[string]bool{}
	list, err := a.Faults(ctx)
	if err != nil {
		a.log.Warn("faults not all listed", "err", err)
	}
	for _, f := range list {
		if kind == "" || f.Kind == kind {
			out[f.Bank] = true
		}
	}
	return out
}

func (a *App) bankRefusing(id string) bool {
	return a.bankPhase(id) == PhaseSuspended || a.faultSet(id).Down(a.clock.Now())
}

func (a *App) takeTimeout(id string) (string, bool) {
	return a.faultSet(id).TakeTimeout()
}

func (a *App) ctlFaults(w http.ResponseWriter, r *http.Request) {
	list, err := a.Faults(r.Context())
	if err != nil {
		a.log.Error("faults", "err", err)
		web.WriteError(w, r, http.StatusServiceUnavailable, "unavailable", "A bank could not be asked about its faults. Run beerus get banks to see its state, then try again.")
		return
	}
	web.WriteJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (a *App) ctlAddFault(w http.ResponseWriter, r *http.Request) {
	var req server.FaultRequest
	if err := web.DecodeBody(r, &req); err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", `Send {"kind":"down","bank":"bank-b","for":"1h"} or {"kind":"timeout","bank":"bank-b","outcome":"credited"}.`)
		return
	}
	f, err := a.AddFault(r.Context(), req)
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
	n, err := a.ClearFaults(r.Context(), bank)
	if err != nil {
		a.log.Error("clear faults", "err", err)
		web.WriteError(w, r, http.StatusServiceUnavailable, "unavailable", "A bank could not clear its faults. Run beerus get banks to see its state, then try again.")
		return
	}
	web.WriteJSON(w, http.StatusOK, map[string]any{"cleared": n})
}
