package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/fleet"
)

const (
	coreID        = "core"
	stopGrace     = 10 * time.Second
	healthTimeout = 3 * time.Second
)

type supervisor struct {
	exe       string
	dir       string
	coreURL   string
	database  string
	ctl       *ctl
	clock     *clock.Clock
	log       *slog.Logger
	logs      *syncWriter
	core      *child
	nibss     *child
	connect   *child
	cbn       *child
	fleet     *fleet.Fleet
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

func newSupervisor(exe, dir, coreURL, databaseURL string, c *ctl, out io.Writer) *supervisor {
	logs := &syncWriter{w: out}
	s := &supervisor{
		exe:      exe,
		dir:      dir,
		coreURL:  coreURL,
		database: databaseURL,
		ctl:      c,
		clock:    clock.New(),
		log:      slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo})),
		logs:     logs,
	}
	s.fleet = fleet.New(c.base, c.token, bankRunner{s}, s.log)
	return s
}

type bankRunner struct {
	s *supervisor
}

func (r bankRunner) Start(id string) (func(), <-chan struct{}) {
	s := r.s
	c := s.launch(id, []string{config.DatabaseURLEnv + "=" + s.database}, []string{"run", "--participant", id, "--dir", s.dir, "--core", s.coreURL, "--supervised"})
	return c.stop, c.done
}

func (s *supervisor) startCore() {
	s.core = s.launch(coreID, nil, []string{"run", "--participant", coreID, "--dir", s.dir, "--supervised"})
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

func (s *supervisor) awaitHealth(ctx context.Context, name string, c *child, health func(context.Context, *http.Client) error) error {
	deadline := time.NewTimer(startTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(startRetry)
	defer tick.Stop()
	client := &http.Client{Timeout: healthTimeout}
	for {
		err := health(ctx, client)
		if err == nil {
			return nil
		}
		select {
		case <-c.firstExit:
			return fmt.Errorf("%s exited before it was ready: %s", name, exitText(c.exit))
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("wait for %s: %w", name, err)
		case <-tick.C:
		}
	}
}

func controlHealth(ctx context.Context, client *http.Client, name string, port int, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/v1/health", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New(name + " answered " + resp.Status)
	}
	return nil
}

func (s *supervisor) watch(ctx context.Context) {
	ctx, s.watchStop = context.WithCancel(ctx)
	s.watchDone = make(chan struct{})
	go func() {
		defer close(s.watchDone)
		s.fleet.Run(ctx)
	}()
}

func (s *supervisor) shutdown() {
	if s.connect != nil {
		s.connect.stop()
		<-s.connect.done
	}
	if s.watchStop != nil {
		s.watchStop()
		<-s.watchDone
	}
	s.fleet.Close()
	for _, c := range []*child{s.nibss, s.core, s.cbn} {
		if c != nil {
			c.stop()
			<-c.done
		}
	}
}
