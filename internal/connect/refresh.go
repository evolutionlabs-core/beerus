package connect

import (
	"context"
	"sync"
	"time"
)

const (
	refreshTimeout  = 5 * time.Second
	refreshFailFast = time.Second
)

type fetchFlight struct {
	done chan struct{}
	err  error
}

type refresher struct {
	mu       sync.Mutex
	flight   *fetchFlight
	failedAt time.Time
	failed   error
}

func (r *refresher) do(ctx context.Context, now func() time.Time, fetch func(context.Context) error) error {
	r.mu.Lock()
	if r.failed != nil && now().Sub(r.failedAt) < refreshFailFast {
		err := r.failed
		r.mu.Unlock()
		return err
	}
	if flight := r.flight; flight != nil {
		r.mu.Unlock()
		select {
		case <-flight.done:
			return flight.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	flight := &fetchFlight{done: make(chan struct{})}
	r.flight = flight
	r.mu.Unlock()
	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
	flight.err = fetch(fetchCtx)
	cancel()
	r.mu.Lock()
	r.flight = nil
	r.failed = flight.err
	if flight.err != nil {
		r.failedAt = now()
	}
	r.mu.Unlock()
	close(flight.done)
	return flight.err
}
