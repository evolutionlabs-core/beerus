package connect

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/countrypack"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/state"
)

const offlineTimeout = 5 * time.Second

type PlanReply = planBody

func PlanOffline(dir string, desired setup.Spec) (PlanReply, error) {
	cfg, err := state.LoadConfig(dir)
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
	ctx, cancel := context.WithTimeout(context.Background(), offlineTimeout)
	defer cancel()
	rows, err := offlineDirectory(ctx, cfg)
	var down *nibssDownError
	switch {
	case errors.As(err, &down) || errors.Is(err, os.ErrNotExist):
		rows = slices.Clone(st.Tombstones)
	case err != nil:
		rows = slices.Clone(st.Tombstones)
		warning = fmt.Sprintf("NIBSS did not answer as expected (%v); the plan checks codes against this setup and the removed banks cached in %s only.", err, state.StatusFile)
	}
	pack, err := countrypack.Nigeria()
	if err != nil {
		return PlanReply{}, fmt.Errorf("load the country pack: %w", err)
	}
	plan := setup.Build(pack, desired, st, rows, clock.New().Today())
	if len(plan.Errors) > 0 {
		return PlanReply{}, planFailure(plan.Errors)
	}
	return PlanReply{Plan: plan, Text: setup.Render(plan), Changed: changes(plan), Warning: warning}, nil
}

func offlineDirectory(ctx context.Context, cfg *config.Config) ([]nip.Institution, error) {
	token, err := config.ReadSecret(filepath.Join(config.NIBSSDir(cfg.Dir), config.NIBSSTokenFile))
	if err != nil {
		return nil, fmt.Errorf("NIBSS control token: %w", err)
	}
	port := cfg.Connect.NIBSSControlPort
	n := &nibssAdmin{link: server.NewLink(token, ""), base: "http://127.0.0.1:" + strconv.Itoa(port), port: port}
	return n.directory(ctx)
}

func keepTombstone(st *setup.Status, in nip.Institution) {
	st.Tombstones = slices.DeleteFunc(st.Tombstones, func(x nip.Institution) bool { return x.ID == in.ID })
	st.Tombstones = append(st.Tombstones, in)
}
