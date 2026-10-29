package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
)

var validName = regexp.MustCompile(`^beerus_[a-z0-9_]{1,50}$`)

type Store struct {
	url  string
	name string
	Pool *pgxpool.Pool
}

func admin(ctx context.Context, url string) (*pgx.Conn, error) {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.Database = "postgres"
	return pgx.ConnectConfig(ctx, cfg)
}

func exists(ctx context.Context, c *pgx.Conn, name string) (bool, error) {
	var ok bool
	err := c.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&ok)
	return ok, err
}

func Open(ctx context.Context, url, name string) (*Store, error) {
	if !validName.MatchString(name) {
		return nil, fmt.Errorf("database name %q must look like beerus_something", name)
	}
	c, err := admin(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("cannot reach Postgres for the bank ledger (%w); start Postgres or set ledger.database_url in beerus.yaml", err)
	}
	defer c.Close(ctx)
	ok, err := exists(ctx, c, name)
	if err != nil {
		return nil, err
	}
	if !ok {
		if _, err := c.Exec(ctx, `CREATE DATABASE `+name); err != nil {
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || (pgErr.Code != "42P04" && pgErr.Code != "23505") {
				return nil, err
			}
		}
	}
	s := &Store{url: url, name: name}
	if err := s.connect(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) connect(ctx context.Context) error {
	cfg, err := pgxpool.ParseConfig(s.url)
	if err != nil {
		return err
	}
	cfg.ConnConfig.Database = s.name
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return err
	}
	if err := ledger.Migrate(ctx, pool); err != nil {
		pool.Close()
		return err
	}
	s.Pool = pool
	return nil
}

func (s *Store) Name() string {
	return s.name
}

func snapName(name, id string) string {
	return name + "_snap_" + id
}

func (s *Store) Snapshot(ctx context.Context, id string) error {
	if !regexp.MustCompile(`^[a-z0-9]{1,24}$`).MatchString(id) {
		return fmt.Errorf("bad snapshot id %q", id)
	}
	s.Pool.Close()
	defer func() {
		if s.Pool != nil {
			_ = s.connect(ctx)
		}
	}()
	c, err := admin(ctx, s.url)
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	_, err = c.Exec(ctx, `CREATE DATABASE `+snapName(s.name, id)+` TEMPLATE `+s.name)
	return err
}

func (s *Store) Restore(ctx context.Context, id string) error {
	if !regexp.MustCompile(`^[a-z0-9]{1,24}$`).MatchString(id) {
		return fmt.Errorf("bad snapshot id %q", id)
	}
	c, err := admin(ctx, s.url)
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	ok, err := exists(ctx, c, snapName(s.name, id))
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no bank database for snapshot %s", id)
	}
	s.Pool.Close()
	if _, err := c.Exec(ctx, `DROP DATABASE IF EXISTS `+s.name+` WITH (FORCE)`); err != nil {
		_ = s.connect(ctx)
		return err
	}
	if _, err := c.Exec(ctx, `CREATE DATABASE `+s.name+` TEMPLATE `+snapName(s.name, id)); err != nil {
		return err
	}
	return s.connect(ctx)
}

func (s *Store) Drop(ctx context.Context) error {
	if s.Pool != nil {
		s.Pool.Close()
	}
	c, err := admin(ctx, s.url)
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	rows, err := c.Query(ctx, `SELECT datname FROM pg_database WHERE datname = $1 OR datname LIKE $2`, s.name, s.name+`\_snap\_%`)
	if err != nil {
		return err
	}
	names := []string{}
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			names = append(names, n)
		}
	}
	rows.Close()
	for _, n := range names {
		if _, err := c.Exec(ctx, `DROP DATABASE IF EXISTS `+n+` WITH (FORCE)`); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Close() {
	if s.Pool != nil {
		s.Pool.Close()
	}
}
