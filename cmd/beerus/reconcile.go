package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/setup"
)

const (
	reconcileEvery = 250 * time.Millisecond
	banksPath      = "/banks"
)

type bankState struct {
	ID       string `json:"id"`
	Phase    string `json:"phase"`
	Port     int    `json:"port"`
	APIPort  int    `json:"api_port"`
	Database string `json:"database"`
}

func (b bankState) fingerprint() string {
	return fmt.Sprintf("%d/%d/%s", b.Port, b.APIPort, b.Database)
}

func (s *supervisor) reconcile(ctx context.Context) {
	tick := time.NewTicker(reconcileEvery)
	defer tick.Stop()
	lastErr := ""
	for {
		banks, err := s.readBanks(ctx)
		switch {
		case err == nil:
			lastErr = ""
			s.sync(banks)
		case err.Error() != lastErr && ctx.Err() == nil:
			s.log.Warn("core is not readable; keeping the banks as they are", "err", err)
			lastErr = err.Error()
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (s *supervisor) readBanks(ctx context.Context) ([]bankState, error) {
	raw, err := s.ctl.request(ctx, "GET", banksPath, nil)
	if err != nil {
		return nil, fmt.Errorf("read banks: %w", err)
	}
	list, err := decode[struct {
		Data []bankState `json:"data"`
	}](raw, "banks")
	if err != nil {
		return nil, err
	}
	if list.Data == nil {
		return nil, errors.New("read banks: the list is missing")
	}
	return list.Data, nil
}

func (s *supervisor) sync(banks []bankState) {
	s.reap()
	listed := map[string]bool{}
	for _, b := range banks {
		listed[b.ID] = true
		running := s.banks[b.ID]
		switch {
		case b.Phase == setup.PhaseClosed:
			s.drop(b.ID)
		case running != nil && running.print != b.fingerprint():
			s.drop(b.ID)
		case running == nil && (b.Phase == setup.PhaseActive || b.Phase == setup.PhaseSuspended):
			s.bring(b.ID, b.fingerprint())
		}
	}
	for _, id := range slices.Sorted(maps.Keys(s.banks)) {
		if !listed[id] {
			s.drop(id)
		}
	}
}

func (s *supervisor) bring(id, print string) {
	if _, busy := s.stopping[id]; busy {
		return
	}
	s.log.Info("starting participant", "participant", id)
	s.banks[id] = s.launch(id, print, []string{"run", "--participant", id, "--dir", s.dir, "--core", s.coreURL, "--supervised"})
	s.order = append(s.order, id)
}

func (s *supervisor) drop(id string) {
	c := s.banks[id]
	if c == nil {
		return
	}
	s.log.Info("stopping participant", "participant", id)
	c.stop()
	delete(s.banks, id)
	s.order = slices.DeleteFunc(s.order, func(o string) bool { return o == id })
	s.stopping[id] = c
}

func (s *supervisor) reap() {
	for id, c := range s.stopping {
		select {
		case <-c.done:
			delete(s.stopping, id)
		default:
		}
	}
}
