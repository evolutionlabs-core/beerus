package connect

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

const offlineTimeout = 5 * time.Second

type PlanReply = planBody

func PlanOffline(dir string, desired setup.Spec, today clock.Date) (PlanReply, error) {
	cfg, err := LoadConfig(dir)
	if err != nil {
		return PlanReply{}, err
	}
	st, err := setup.LoadStatus(dir)
	if err != nil {
		return PlanReply{}, err
	}
	if errs := setup.Validate(desired); len(errs) > 0 {
		return PlanReply{}, planFailure(errs)
	}
	warning := ""
	rows, err := offlineDirectory(cfg)
	if err != nil {
		rows = slices.Clone(st.Tombstones)
		warning = fmt.Sprintf("NIBSS is not reachable (%v); the plan uses only the removed banks cached in %s, so it cannot check codes against live banks.", err, statusFile)
	}
	plan := setup.Build(desired, st, rows, today)
	if len(plan.Errors) > 0 {
		return PlanReply{}, planFailure(plan.Errors)
	}
	return PlanReply{Plan: plan, Text: setup.Render(plan), Changed: changes(plan), Warning: warning}, nil
}

func offlineDirectory(cfg *config.Config) ([]nibss.Institution, error) {
	ctx, cancel := context.WithTimeout(context.Background(), offlineTimeout)
	defer cancel()
	name := config.DatabaseName(cfg.Dir, setup.SystemNIBSS)
	found, err := store.List(ctx, cfg.DatabaseURL, name)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(found, name) {
		return nil, fmt.Errorf("the NIBSS database %s does not exist", name)
	}
	s, err := nibss.Open(ctx, cfg.DatabaseURL, name)
	if err != nil {
		return nil, fmt.Errorf("open the NIBSS directory: %w", err)
	}
	defer s.Close()
	return s.Directory(ctx)
}

func keepTombstone(st *setup.Status, in nibss.Institution) {
	st.Tombstones = slices.DeleteFunc(st.Tombstones, func(x nibss.Institution) bool { return x.ID == in.ID })
	st.Tombstones = append(st.Tombstones, in)
}
