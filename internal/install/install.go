package install

import (
	"context"
	"errors"
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/seed"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/state"
)

const postgresTimeout = 5 * time.Second

const (
	stepNamePostgres = "Checking Postgres"
	stepNameConfig   = "Writing config"
	stepNameClaims   = "Reserving CBN and NIBSS database names"
	stepNameCBN      = "Preparing CBN ports and certificates"
	detailWritten    = "already written"
)

type InstallStep struct {
	Name     string
	Detail   string
	Duration time.Duration
	Err      error
}

func InstallLabels() []string {
	return []string{stepNamePostgres, stepNameConfig + " (" + detailWritten + ")", stepNameClaims, stepNameCBN}
}

type installer struct {
	clock  *clock.Clock
	report func(InstallStep)
}

func (i *installer) run(ctx context.Context, name string, fn func() (string, error)) error {
	start := i.clock.Real()
	detail, err := "", ctx.Err()
	if err == nil {
		detail, err = fn()
	}
	i.report(InstallStep{Name: name, Detail: detail, Duration: i.clock.Real().Sub(start), Err: err})
	return err
}

func Install(ctx context.Context, dir string, spec setup.Spec, local setup.Local, today clock.Date, report func(InstallStep)) error {
	st, found, err := loadSetup(dir)
	if err != nil {
		return err
	}
	if found && systemPhase(st, setup.SystemCBN) == config.PhaseActive && systemPhase(st, setup.SystemNIBSS) == config.PhaseActive {
		return fmt.Errorf("%s is already set up. Run beerus up to start it.", dir)
	}
	in := &installer{clock: clock.New(), report: report}
	url := local.DatabaseURL
	if found {
		if local, err = setup.LoadLocal(dir); err != nil {
			return err
		}
		url = local.DatabaseURL
	} else if u := os.Getenv(config.DatabaseURLEnv); u != "" {
		url = u
	}
	if err := in.run(ctx, stepNamePostgres, func() (string, error) { return "", checkPostgres(ctx, url) }); err != nil {
		return err
	}
	err = in.run(ctx, stepNameConfig, func() (string, error) {
		if found {
			return detailWritten, nil
		}
		return "", seed.Init(dir, spec, local, today)
	})
	if err != nil {
		return err
	}
	cfg, err := setup.LoadConfig(dir)
	if err != nil {
		return err
	}
	ctl, err := state.Open(dir)
	if err != nil {
		return err
	}
	err = in.run(ctx, stepNameClaims, func() (string, error) { return "", claimSystems(ctl, cfg) })
	if err != nil {
		return err
	}
	err = in.run(ctx, stepNameCBN, func() (string, error) { return "", prepareCBN(ctl, cfg, in.clock.Real()) })
	if err != nil {
		return err
	}
	return activate(ctl)
}

func checkPostgres(ctx context.Context, url string) error {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return fmt.Errorf("read the database url: %w", err)
	}
	cfg.Database = "postgres"
	ctx, cancel := context.WithTimeout(ctx, postgresTimeout)
	defer cancel()
	hostPort := net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port)))
	c, err := pgx.ConnectConfig(ctx, cfg)
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &pgErr):
		return fmt.Errorf("Postgres at %s refused the connection: %s", hostPort, pgErr.Message)
	case err != nil:
		return fmt.Errorf("cannot reach Postgres at %s. Postgres is not installed or not running.", hostPort)
	}
	return c.Close(ctx)
}

func claimSystem(ctl *state.Store, cfg *config.Config, id string) error {
	return ctl.Claim(setup.KindSystem, id, setup.Owned{Kind: state.OwnedDatabase, Name: config.DatabaseName(cfg.Dir, id)})
}

func claimSystems(ctl *state.Store, cfg *config.Config) error {
	for _, id := range []string{setup.SystemCBN, setup.SystemNIBSS} {
		if err := claimSystem(ctl, cfg, id); err != nil {
			return err
		}
	}
	return nil
}

func prepareCBN(ctl *state.Store, cfg *config.Config, now time.Time) error {
	if err := state.EnsureCBNPorts(ctl, cfg); err != nil {
		return err
	}
	if err := cfg.EnsureCBNDir(now); err != nil {
		return fmt.Errorf("ensure the CBN directory: %w", err)
	}
	return nil
}

func activate(ctl *state.Store) error {
	for _, id := range []string{setup.SystemCBN, setup.SystemNIBSS} {
		if err := state.MarkActive(ctl, id); err != nil {
			return err
		}
	}
	return nil
}
