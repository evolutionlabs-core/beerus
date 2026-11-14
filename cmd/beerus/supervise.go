package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

const (
	coreID    = "core"
	stopGrace = 10 * time.Second
)

type supervisor struct {
	exe       string
	dir       string
	coreURL   string
	ctl       *ctl
	clock     *clock.Clock
	log       *slog.Logger
	logs      *syncWriter
	core      *child
	banks     map[string]*child
	order     []string
	stopping  map[string]*child
	watchStop context.CancelFunc
	watchDone chan struct{}
}

type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func newSupervisor(exe, dir, coreURL string, c *ctl, out io.Writer) *supervisor {
	logs := &syncWriter{w: out}
	return &supervisor{
		exe:      exe,
		dir:      dir,
		coreURL:  coreURL,
		ctl:      c,
		clock:    clock.New(nil),
		log:      slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo})),
		logs:     logs,
		banks:    map[string]*child{},
		stopping: map[string]*child{},
	}
}

func (s *supervisor) startCore() {
	s.core = s.launch(coreID, "", []string{"run", "--participant", coreID, "--dir", s.dir, "--supervised"})
}

func (s *supervisor) awaitCore(ctx context.Context) error {
	deadline := time.NewTimer(startTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(startRetry)
	defer tick.Stop()
	for {
		_, err := s.ctl.request(ctx, "GET", "/health", nil)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !errors.Is(err, errNotRunning) {
			return fmt.Errorf("check core health: %w", err)
		}
		select {
		case <-s.core.firstExit:
			return fmt.Errorf("core exited before it was ready: %s", exitText(s.core.exit))
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("wait for core: %w", err)
		case <-tick.C:
		}
	}
}

func (s *supervisor) watch(ctx context.Context) {
	ctx, s.watchStop = context.WithCancel(ctx)
	s.watchDone = make(chan struct{})
	go func() {
		defer close(s.watchDone)
		s.reconcile(ctx)
	}()
}

func (s *supervisor) shutdown() {
	if s.watchStop != nil {
		s.watchStop()
		<-s.watchDone
	}
	for _, id := range s.order {
		s.banks[id].stop()
	}
	for _, id := range slices.Backward(slices.Clone(s.order)) {
		<-s.banks[id].done
	}
	for _, c := range s.stopping {
		<-c.done
	}
	s.core.stop()
	<-s.core.done
}
