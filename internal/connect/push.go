package connect

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/clock"
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
	config string
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

func (a *App) wishFor(id string) (bankWish, error) {
	w := bankWish{}
	var err error
	if w.config, err = a.ensureBankDir(id, false); err != nil {
		return w, err
	}
	w.phase, w.reason = a.bankPhaseReason(id)
	return w, nil
}

func wishGeneration(world string, w bankWish) int64 {
	h := sha256.New()
	for _, part := range []string{world, w.phase, w.reason, w.config} {
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
		w, err := a.wishFor(id)
		if err == nil {
			err = a.syncBank(ctx, id, base, wishGeneration(a.WorldID(), w), w)
		}
		delay := pushInSync
		switch {
		case err == nil:
			attempts, warned = 0, ""
		case ctx.Err() != nil:
			return
		default:
			attempts++
			delay = min(clock.Backoff(attempts), pushRetryMax)
			if msg := err.Error(); warned != msg {
				warned = msg
				a.log.Warn("bank not synced; retrying", "bank", id, "err", err)
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
	var state server.State
	if _, err := a.link.Request(ctx, base, http.MethodGet, "/v1/status", nil, nil, &state); err != nil {
		return fmt.Errorf("read status of %s: %w", id, err)
	}
	if state.ObservedGeneration == gen && state.Phase == w.phase {
		return nil
	}
	body := server.PhaseRequest{Phase: w.phase, Reason: w.reason, Generation: gen, WorldID: a.WorldID()}
	if _, err := a.link.Request(ctx, base, http.MethodPut, "/v1/phase", nil, body, nil); err != nil {
		return fmt.Errorf("send the phase of %s: %w", id, err)
	}
	return nil
}
