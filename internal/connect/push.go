package connect

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	pushEvery    = 500 * time.Millisecond
	pushTimeout  = 5 * time.Second
	pushRetryMax = 5 * time.Second
)

type bankWish struct {
	phase  string
	reason string
	downs  []Fault
	print  string
}

type pushState struct {
	gen      int64
	print    string
	attempts int
	next     time.Time
	warned   string
}

type remoteFaults struct {
	Data       []participantFault `json:"data"`
	NextCursor string             `json:"next_cursor"`
}

func (a *App) pokePush() {
	select {
	case a.pushWake <- struct{}{}:
	default:
	}
}

func pushable(phase string) bool {
	return phase != PhaseProvisioning && phase != PhaseClosed
}

func (a *App) wishFor(id string) bankWish {
	w := bankWish{phase: a.bankPhase(id)}
	a.lifeMu.Lock()
	if st := a.status[id]; st != nil {
		w.reason = st.Reason
	}
	a.lifeMu.Unlock()
	ids := []string{}
	for _, f := range a.Faults() {
		if f.Kind == FaultDown && f.Bank == id {
			w.downs = append(w.downs, f)
			ids = append(ids, f.ID)
		}
	}
	w.print = strings.Join(append([]string{w.phase, w.reason}, ids...), "\x00")
	return w
}

func (a *App) runPush(ctx context.Context) {
	states := map[string]*pushState{}
	tick := time.NewTicker(pushEvery)
	defer tick.Stop()
	for {
		a.pushAll(ctx, states)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-a.pushWake:
		}
	}
}

func (a *App) pushAll(ctx context.Context, states map[string]*pushState) {
	listed := map[string]bool{}
	for _, b := range a.bankList() {
		w := a.wishFor(b.ID)
		if !pushable(w.phase) {
			continue
		}
		listed[b.ID] = true
		st := states[b.ID]
		if st == nil {
			st = &pushState{gen: a.clock.Real().UnixNano()}
			states[b.ID] = st
		}
		if st.print != w.print {
			st.gen++
			st.print, st.attempts, st.next = w.print, 0, time.Time{}
		}
		if ctx.Err() != nil || a.clock.Real().Before(st.next) {
			continue
		}
		err := a.syncBank(ctx, b.ID, b.URL(), st.gen, w)
		if err == nil {
			st.attempts, st.warned = 0, ""
			continue
		}
		st.attempts++
		st.next = a.clock.Real().Add(min(backoff(st.attempts), pushRetryMax))
		if msg := err.Error(); st.warned != msg && ctx.Err() == nil {
			st.warned = msg
			a.log.Warn("bank not reachable; retrying", "bank", b.ID, "err", err)
		}
	}
	for id := range states {
		if !listed[id] {
			delete(states, id)
		}
	}
}

func (a *App) syncBank(ctx context.Context, id, base string, gen int64, w bankWish) error {
	ctx, cancel := context.WithTimeout(ctx, pushTimeout)
	defer cancel()
	var state participantState
	if _, err := a.peerRequest(ctx, base, http.MethodGet, "/v1/status", nil, nil, &state); err != nil {
		return fmt.Errorf("read status of %s: %w", id, err)
	}
	if state.ObservedGeneration == gen && state.Phase == w.phase {
		return nil
	}
	if err := a.syncFaults(ctx, id, base, w.downs); err != nil {
		return err
	}
	body := phaseRequest{Phase: w.phase, Reason: w.reason, Generation: gen, WorldID: a.WorldID()}
	if _, err := a.peerRequest(ctx, base, http.MethodPut, "/v1/phase", nil, body, nil); err != nil {
		return fmt.Errorf("send the phase of %s: %w", id, err)
	}
	return nil
}

func (a *App) heldFaults(ctx context.Context, base string) ([]string, error) {
	held := []string{}
	cursor := ""
	for {
		path := "/v1/faults?limit=" + strconv.Itoa(faultPageMax)
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		var page remoteFaults
		if _, err := a.peerRequest(ctx, base, http.MethodGet, path, nil, nil, &page); err != nil {
			return nil, err
		}
		for _, f := range page.Data {
			held = append(held, f.ID)
		}
		if page.NextCursor == "" {
			return held, nil
		}
		cursor = page.NextCursor
	}
}

func (a *App) syncFaults(ctx context.Context, id, base string, downs []Fault) error {
	held, err := a.heldFaults(ctx, base)
	if err != nil {
		return fmt.Errorf("list faults of %s: %w", id, err)
	}
	wanted := make([]string, 0, len(downs))
	for _, f := range downs {
		wanted = append(wanted, faultIDFor(f.ID))
	}
	if slices.ContainsFunc(held, func(h string) bool { return !slices.Contains(wanted, h) }) {
		if _, err := a.peerRequest(ctx, base, http.MethodDelete, "/v1/faults", nil, nil, nil); err != nil {
			return fmt.Errorf("clear faults of %s: %w", id, err)
		}
		held = nil
	}
	now := a.clock.Now()
	for i, f := range downs {
		if slices.Contains(held, wanted[i]) {
			continue
		}
		req := FaultRequest{Kind: FaultDown}
		if f.Until != nil {
			left := f.Until.Sub(now).Round(time.Second)
			if left <= 0 {
				continue
			}
			req.For = min(left, maxFaultFor).String()
		}
		header := http.Header{"Idempotency-Key": {f.ID}}
		_, err := a.peerRequest(ctx, base, http.MethodPost, "/v1/faults", header, req, nil)
		var pe *peerError
		if errors.As(err, &pe) && pe.Code == "idempotency_key_reused" {
			continue
		}
		if err != nil {
			return fmt.Errorf("send fault %s to %s: %w", f.ID, id, err)
		}
	}
	return nil
}
