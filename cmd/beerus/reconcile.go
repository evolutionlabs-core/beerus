package main

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/setup"
)

const (
	reconcileEvery = time.Second
	replicaPath    = "/internal/replica"
)

type replicaView struct {
	Phases map[string]string `json:"phases"`
}

func (s *supervisor) reconcile(ctx context.Context) {
	tick := time.NewTicker(reconcileEvery)
	defer tick.Stop()
	lastErr := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		phases, err := s.phases()
		if err != nil {
			if msg := err.Error(); msg != lastErr {
				s.log.Warn("core is not readable; keeping the banks as they are", "err", err)
				lastErr = msg
			}
			continue
		}
		lastErr = ""
		s.sync(ctx, phases)
	}
}

func (s *supervisor) phases() (map[string]string, error) {
	raw, err := s.ctl.send("GET", replicaPath, nil)
	if err != nil {
		return nil, fmt.Errorf("read replica: %w", err)
	}
	rep, err := decode[replicaView](raw, "replica")
	if err != nil {
		return nil, err
	}
	return rep.Phases, nil
}

func (s *supervisor) sync(ctx context.Context, phases map[string]string) {
	for _, id := range slices.Sorted(maps.Keys(phases)) {
		switch phase := phases[id]; {
		case phase == setup.PhaseClosed || phase == setup.PhaseTerminating:
			s.drop(id)
		case s.banks[id] == nil && (phase == setup.PhaseActive || phase == setup.PhaseSuspended):
			s.bring(ctx, id)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(s.banks)) {
		if _, ok := phases[id]; !ok {
			s.drop(id)
		}
	}
}

func (s *supervisor) bring(ctx context.Context, id string) {
	s.log.Info("starting participant", "participant", id)
	s.banks[id] = s.launch(ctx, id, []string{"run", "--participant", id, "--dir", s.dir, "--core", s.coreURL, "--supervised"})
	s.order = append(s.order, id)
}

func (s *supervisor) drop(id string) {
	c := s.banks[id]
	if c == nil {
		return
	}
	s.log.Info("stopping participant", "participant", id)
	c.stop()
	<-c.done
	delete(s.banks, id)
	s.order = slices.DeleteFunc(s.order, func(o string) bool { return o == id })
}
