package connect

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"time"
)

const (
	pushEvery      = 500 * time.Millisecond
	pushInSync     = 2 * time.Second
	pushTimeout    = 5 * time.Second
	pushRetryMax   = 5 * time.Second
	generationMask = 1<<62 - 1
)

type bankWish struct {
	phase  string
	reason string
	downs  []Fault
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
	w := bankWish{}
	w.phase, w.reason = a.bankPhaseReason(id)
	for _, f := range a.Faults() {
		if f.Kind == FaultDown && f.Bank == id {
			w.downs = append(w.downs, f)
		}
	}
	return w
}

func wishGeneration(world string, w bankWish) int64 {
	ids := make([]string, 0, len(w.downs))
	for _, f := range w.downs {
		ids = append(ids, f.ID)
	}
	slices.Sort(ids)
	h := sha256.New()
	for _, part := range append([]string{world, w.phase, w.reason}, ids...) {
		fmt.Fprintf(h, "%d:%s", len(part), part)
	}
	gen := int64(binary.BigEndian.Uint64(h.Sum(nil)[:8]) & generationMask)
	if gen == 0 {
		return 1
	}
	return gen
}

type bankPusher struct {
	wake   chan struct{}
	cancel context.CancelFunc
}

func (a *App) runPush(ctx context.Context) {
	pushers := map[string]*bankPusher{}
	var wg sync.WaitGroup
	tick := time.NewTicker(pushEvery)
	defer tick.Stop()
	for {
		a.startPushers(ctx, &wg, pushers)
		select {
		case <-ctx.Done():
			for _, p := range pushers {
				p.cancel()
			}
			wg.Wait()
			return
		case <-tick.C:
		case <-a.pushWake:
			for _, p := range pushers {
				select {
				case p.wake <- struct{}{}:
				default:
				}
			}
		}
	}
}

func (a *App) startPushers(ctx context.Context, wg *sync.WaitGroup, pushers map[string]*bankPusher) {
	listed := map[string]bool{}
	for _, b := range a.bankList() {
		if !pushable(a.bankPhase(b.ID)) {
			continue
		}
		listed[b.ID] = true
		if pushers[b.ID] != nil {
			continue
		}
		bctx, cancel := context.WithCancel(ctx)
		p := &bankPusher{wake: make(chan struct{}, 1), cancel: cancel}
		pushers[b.ID] = p
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.pushBank(bctx, b.ID, b.URL(), p.wake)
		}()
	}
	for id, p := range pushers {
		if !listed[id] {
			p.cancel()
			delete(pushers, id)
		}
	}
}

func (a *App) pushBank(ctx context.Context, id, base string, wake <-chan struct{}) {
	attempts := 0
	warned := ""
	for {
		w := a.wishFor(id)
		err := a.syncBank(ctx, id, base, wishGeneration(a.WorldID(), w), w)
		delay := pushInSync
		switch {
		case err == nil:
			attempts, warned = 0, ""
		case ctx.Err() != nil:
			return
		default:
			attempts++
			delay = min(backoff(attempts), pushRetryMax)
			if msg := err.Error(); warned != msg {
				warned = msg
				a.log.Warn("bank not reachable; retrying", "bank", id, "err", err)
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-wake:
			timer.Stop()
		case <-timer.C:
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
