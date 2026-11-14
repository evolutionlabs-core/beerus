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
	replicaPath    = "/internal/replica"
)

type replicaBank struct {
	ID       string
	Port     int
	APIPort  int
	Database string
}

type replicaView struct {
	Banks  []replicaBank     `json:"banks"`
	Phases map[string]string `json:"phases"`
}

func (b replicaBank) fingerprint() string {
	return fmt.Sprintf("%d/%d/%s", b.Port, b.APIPort, b.Database)
}

func (s *supervisor) reconcile(ctx context.Context) {
	tick := time.NewTicker(reconcileEvery)
	defer tick.Stop()
	lastErr := ""
	for {
		rep, err := s.replica(ctx)
		switch {
		case err == nil:
			lastErr = ""
			s.sync(rep)
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

func (s *supervisor) replica(ctx context.Context) (replicaView, error) {
	raw, err := s.ctl.request(ctx, "GET", replicaPath, nil)
	if err != nil {
		return replicaView{}, fmt.Errorf("read replica: %w", err)
	}
	rep, err := decode[replicaView](raw, "replica")
	if err != nil {
		return replicaView{}, err
	}
	if rep.Phases == nil || rep.Banks == nil {
		return replicaView{}, errors.New("read replica: it has no banks or phases")
	}
	return rep, nil
}

func (s *supervisor) sync(rep replicaView) {
	s.reap()
	prints := map[string]string{}
	for _, b := range rep.Banks {
		prints[b.ID] = b.fingerprint()
	}
	for _, id := range slices.Sorted(maps.Keys(rep.Phases)) {
		running := s.banks[id]
		switch phase := rep.Phases[id]; {
		case phase == setup.PhaseClosed || phase == setup.PhaseTerminating:
			s.drop(id)
		case running != nil && running.print != prints[id]:
			s.drop(id)
		case running == nil && (phase == setup.PhaseActive || phase == setup.PhaseSuspended):
			s.bring(id, prints[id])
		}
	}
	for _, id := range slices.Sorted(maps.Keys(s.banks)) {
		if _, ok := rep.Phases[id]; !ok {
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
