package connect

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

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
	now := a.clock.Now()
	f, err := server.NewFault(req, now)
	if err != nil {
		return server.Fault{}, err
	}
	f.Bank = req.Bank
	switch {
	case f.Kind == server.FaultDown:
		f.ID = server.FaultID(fmt.Sprintf("%s|%s|%d", f.Bank, f.Kind, f.At.UnixNano()))
		a.mu.Lock()
		a.st.Faults = append(a.st.Faults, &f)
		a.dirty()
		a.mu.Unlock()
		a.pokePush()
		return f, nil
	case a.role != RoleCore:
		f.ID = server.FaultID(web.NewID("flt"))
		a.faultSet(b.ID).Add(f, now)
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

func (a *App) downFaults(id string) []server.Fault {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.clock.Now()
	out := []server.Fault{}
	for _, f := range a.st.Faults {
		if f.Bank == id && f.Kind == server.FaultDown && f.Live(now) {
			out = append(out, *f)
		}
	}
	return out
}

func (a *App) Faults(ctx context.Context) ([]server.Fault, error) {
	a.mu.Lock()
	now := a.clock.Now()
	out := []server.Fault{}
	for _, f := range a.st.Faults {
		if f.Live(now) {
			out = append(out, *f)
		}
	}
	a.mu.Unlock()
	timeouts, err := a.timeoutFaults(ctx)
	return append(out, timeouts...), err
}

func (a *App) timeoutFaults(ctx context.Context) ([]server.Fault, error) {
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
		list, err := a.bankTimeouts(ctx, b)
		if err != nil {
			errs = append(errs, fmt.Errorf("list faults of %s: %w", b.ID, err))
			continue
		}
		out = append(out, list...)
	}
	return out, errors.Join(errs...)
}

func (a *App) bankTimeouts(ctx context.Context, b *config.Bank) ([]server.Fault, error) {
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
			if v.Kind == server.FaultTimeout {
				out = append(out, faultOf(b.ID, v))
			}
		}
		if page.NextCursor == "" {
			return out, nil
		}
		cursor = page.NextCursor
	}
}

func (a *App) ClearFaults(ctx context.Context, bank string) (int, error) {
	a.mu.Lock()
	now := a.clock.Now()
	cleared := 0
	keep := a.st.Faults[:0]
	for _, f := range a.st.Faults {
		switch {
		case !f.Live(now):
		case bank == "" || f.Bank == bank:
			cleared++
		default:
			keep = append(keep, f)
		}
	}
	a.st.Faults = keep
	a.dirty()
	a.mu.Unlock()
	a.pokePush()
	var errs []error
	for _, b := range a.bankList() {
		if bank != "" && b.ID != bank {
			continue
		}
		n, err := a.clearTimeouts(ctx, b)
		cleared += n
		errs = append(errs, err)
	}
	return cleared, errors.Join(errs...)
}

func (a *App) clearTimeouts(ctx context.Context, b *config.Bank) (int, error) {
	if a.role != RoleCore {
		return a.faultSet(b.ID).Clear(a.clock.Now()), nil
	}
	if !pushable(a.bankPhase(b.ID)) {
		return 0, nil
	}
	held, err := a.bankTimeouts(ctx, b)
	if err != nil {
		return 0, fmt.Errorf("list faults of %s: %w", b.ID, err)
	}
	if len(held) == 0 {
		return 0, nil
	}
	if _, err := a.link.Request(ctx, b.URL(), http.MethodDelete, "/v1/faults", nil, nil, nil); err != nil {
		return 0, fmt.Errorf("clear faults of %s: %w", b.ID, err)
	}
	return len(held), nil
}

func (a *App) downBanks() map[string]bool {
	out := map[string]bool{}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.clock.Now()
	for _, f := range a.st.Faults {
		if f.Live(now) && f.Kind == server.FaultDown {
			out[f.Bank] = true
		}
	}
	return out
}

func (a *App) faultBanks(ctx context.Context) (map[string]bool, error) {
	out := a.downBanks()
	timeouts, err := a.timeoutFaults(ctx)
	if err != nil {
		return nil, err
	}
	for _, f := range timeouts {
		out[f.Bank] = true
	}
	return out, nil
}

func (a *App) bankDown(id string) bool {
	return len(a.downFaults(id)) > 0
}

func (a *App) bankRefusing(id string) bool {
	return a.bankPhase(id) == PhaseSuspended || a.bankDown(id)
}

func (a *App) takeTimeout(id string) (string, bool) {
	return a.faultSet(id).TakeTimeout()
}

func (a *App) pruneFaultsLocked(now time.Time) {
	a.st.Faults = slices.DeleteFunc(a.st.Faults, func(f *server.Fault) bool { return !f.Live(now) })
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
