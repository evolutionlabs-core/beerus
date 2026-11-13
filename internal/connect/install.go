package connect

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

const postgresTimeout = 5 * time.Second

const (
	stepNamePostgres   = "Checking Postgres"
	stepNameConfig     = "Writing config to "
	stepNameCBNCreate  = "Creating CBN database"
	stepNameCBNMigrate = "Migrating CBN ledger"
	stepNameCBNOpen    = "Opening CBN settlement ledger"
	stepNameNIBSSMake  = "Creating NIBSS database"
	stepNameNIBSSMigr  = "Migrating NIBSS schema"
)

type InstallStep struct {
	Name     string
	Detail   string
	Duration time.Duration
	Err      error
}

func InstallLabels(dir string) []string {
	longest := " (" + migrationCount(0) + ")"
	return []string{stepNamePostgres, stepNameConfig + dir + longest, stepNameCBNCreate, stepNameCBNMigrate + longest, stepNameCBNOpen, stepNameNIBSSMake, stepNameNIBSSMigr + longest}
}

func CheckStart(dir string) error {
	st, found, err := loadSetup(dir)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("no Beerus setup in %s. Run beerus init first.", dir)
	}
	if systemPhase(st, setup.SystemCBN) == setup.PhaseProvisioning {
		return fmt.Errorf("the setup in %s is not finished. Run beerus init again to finish it.", dir)
	}
	return nil
}

func loadSetup(dir string) (setup.Status, bool, error) {
	ok, err := Initialized(dir)
	if err != nil || !ok {
		return setup.Status{}, false, err
	}
	st, err := setup.LoadStatus(dir)
	if err != nil {
		return setup.Status{}, false, err
	}
	return st, true, nil
}

func systemPhase(st setup.Status, id string) string {
	if r := findResource(&st, setup.KindSystem, id); r != nil {
		return r.Phase
	}
	return ""
}

func stepRecorded(st setup.Status, system, step string) bool {
	r := findResource(&st, setup.KindSystem, system)
	return r != nil && r.Steps[step] == stepDone
}

type installer struct {
	clock  *clock.Clock
	report func(InstallStep)
}

func (i *installer) run(name string, fn func() (string, error)) error {
	start := i.clock.Real()
	detail, err := fn()
	i.report(InstallStep{Name: name, Detail: detail, Duration: i.clock.Real().Sub(start), Err: err})
	return err
}

func Install(ctx context.Context, dir string, spec setup.Spec, local setup.Local, today clock.Date, report func(InstallStep)) error {
	st, found, err := loadSetup(dir)
	if err != nil {
		return err
	}
	if found && systemPhase(st, setup.SystemCBN) == setup.PhaseActive && systemPhase(st, setup.SystemNIBSS) == setup.PhaseActive {
		return fmt.Errorf("%s is already set up. Run beerus up to start it.", dir)
	}
	in := &installer{clock: clock.New(nil), report: report}
	url := local.DatabaseURL
	if found {
		if local, err = setup.LoadLocal(dir); err != nil {
			return err
		}
		url = local.DatabaseURL
	} else if u := os.Getenv(config.DatabaseURLEnv); u != "" {
		url = u
	}
	if err := in.run(stepNamePostgres, func() (string, error) { return "", checkPostgres(ctx, url) }); err != nil {
		return err
	}
	err = in.run(stepNameConfig+dir, func() (string, error) {
		if found {
			return "already written", nil
		}
		return "", Init(dir, spec, local, today)
	})
	if err != nil {
		return err
	}
	cfg, err := LoadConfig(dir)
	if err != nil {
		return err
	}
	ctl, err := openStore(dir)
	if err != nil {
		return err
	}
	if st, err = ctl.status(); err != nil {
		return err
	}
	if err := installCBN(ctx, in, cfg, ctl, st); err != nil {
		return err
	}
	return installNIBSS(ctx, in, cfg, ctl, st)
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

func installCBN(ctx context.Context, in *installer, cfg *config.Config, ctl *ctlStore, st setup.Status) error {
	name := config.DatabaseName(cfg.Dir, cbnID)
	steps := []string{setup.StepDatabase, setup.StepLedgerSchema, setup.StepSettlement}
	err := in.ensure(ctl, st, setup.SystemCBN, steps[0], stepNameCBNCreate, steps, func() (string, error) {
		if err := ctl.Claim(setup.KindSystem, setup.SystemCBN, setup.Owned{Kind: ownedDatabase, Name: name}); err != nil {
			return "", err
		}
		return "", store.Create(ctx, cfg.DatabaseURL, name)
	})
	if err != nil {
		return err
	}
	err = in.ensure(ctl, st, setup.SystemCBN, steps[1], stepNameCBNMigrate, steps, func() (string, error) {
		n, err := store.Migrate(ctx, cfg.DatabaseURL, name, ledger.Migrate)
		return migrationCount(n), err
	})
	if err != nil {
		return err
	}
	return in.ensure(ctl, st, setup.SystemCBN, steps[2], stepNameCBNOpen, steps, func() (string, error) {
		r, err := openRTGS(ctx, cfg, ctl, cfg.Start)
		if err != nil {
			return "", err
		}
		r.Close()
		return "", nil
	})
}

func installNIBSS(ctx context.Context, in *installer, cfg *config.Config, ctl *ctlStore, st setup.Status) error {
	name := config.DatabaseName(cfg.Dir, setup.SystemNIBSS)
	steps := []string{setup.StepDatabase, setup.StepSchema}
	err := in.ensure(ctl, st, setup.SystemNIBSS, steps[0], stepNameNIBSSMake, steps, func() (string, error) {
		if err := ctl.Claim(setup.KindSystem, setup.SystemNIBSS, setup.Owned{Kind: ownedDatabase, Name: name}); err != nil {
			return "", err
		}
		return "", nibss.Create(ctx, cfg.DatabaseURL, name)
	})
	if err != nil {
		return err
	}
	return in.ensure(ctl, st, setup.SystemNIBSS, steps[1], stepNameNIBSSMigr, steps, func() (string, error) {
		n, err := nibss.Migrate(ctx, cfg.DatabaseURL, name)
		return migrationCount(n), err
	})
}

func (in *installer) ensure(ctl *ctlStore, st setup.Status, system, step, name string, all []string, fn func() (string, error)) error {
	return in.run(name, func() (string, error) {
		if stepRecorded(st, system, step) {
			return skippedDetail(step), nil
		}
		detail, err := fn()
		if err != nil {
			return "", err
		}
		return detail, ctl.update(func(s *setup.Status) (bool, error) {
			r := ensureResource(s, setup.KindSystem, system)
			if r.Steps == nil {
				r.Steps = map[string]string{}
			}
			r.Steps[step] = stepDone
			if slices.ContainsFunc(all, func(id string) bool { return r.Steps[id] != stepDone }) {
				return true, nil
			}
			r.Phase = setup.PhaseActive
			return true, nil
		})
	})
}

func skippedDetail(step string) string {
	if step == setup.StepLedgerSchema || step == setup.StepSchema {
		return migrationCount(0)
	}
	return ""
}
