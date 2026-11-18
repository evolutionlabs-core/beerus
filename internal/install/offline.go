package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/countrypack"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

const offlineTimeout = 5 * time.Second

type PlanReply struct {
	setup.Plan
	Text    string `json:"text"`
	Changed bool   `json:"changed"`
	Warning string `json:"warning,omitempty"`
}

func PlanOffline(ctx context.Context, dir string, desired setup.Spec) (PlanReply, error) {
	cfg, err := setup.LoadConfig(dir)
	if err != nil {
		return PlanReply{}, err
	}
	st, err := setup.LoadStatus(dir)
	if err != nil {
		return PlanReply{}, err
	}
	if errs := setup.Validate(desired); len(errs) > 0 {
		return PlanReply{}, errors.Join(errs...)
	}
	warning := ""
	ctx, cancel := context.WithTimeout(ctx, offlineTimeout)
	defer cancel()
	rows, err := offlineDirectory(ctx, cfg)
	var down *server.NIBSSDownError
	switch {
	case errors.As(err, &down) || errors.Is(err, os.ErrNotExist):
		rows = slices.Clone(st.Tombstones)
	case err != nil:
		rows = slices.Clone(st.Tombstones)
		warning = fmt.Sprintf("NIBSS did not answer as expected (%v); the plan checks codes against this setup and the removed banks cached in %s only.", err, setup.StatusFile)
	}
	pack, err := countrypack.Nigeria()
	if err != nil {
		return PlanReply{}, fmt.Errorf("load the country pack: %w", err)
	}
	plan := setup.Build(pack, desired, st, rows, clock.New().Today())
	if len(plan.Errors) > 0 {
		return PlanReply{}, errors.Join(plan.Errors...)
	}
	return PlanReply{Plan: plan, Text: setup.Render(plan), Changed: plan.Changes(), Warning: warning}, nil
}

func offlineDirectory(ctx context.Context, cfg *config.Config) ([]nip.Institution, error) {
	token, err := config.ReadSecret(filepath.Join(config.NIBSSDir(cfg.Dir), config.NIBSSTokenFile))
	if err != nil {
		return nil, fmt.Errorf("NIBSS control token: %w", err)
	}
	return server.NewNIBSSControl(server.NewLink(token, ""), cfg.Connect.NIBSSControlPort).Directory(ctx)
}
