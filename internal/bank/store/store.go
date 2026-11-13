package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
)

var validName = regexp.MustCompile(`^beerus_[a-z0-9_]{1,50}$`)

type Migration func(context.Context, *pgxpool.Pool) (int, error)

type Store struct {
	url        string
	name       string
	Pool       *pgxpool.Pool
	migrations []Migration
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

func Create(ctx context.Context, url, name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("database name %q must look like beerus_something", name)
	}
	c, err := admin(ctx, url)
	if err != nil {
		return fmt.Errorf("cannot reach Postgres for the bank ledger (%w); start Postgres or set database_url in local.yaml", err)
	}
	defer c.Close(ctx)
	ok, err := exists(ctx, c, name)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	if _, err := c.Exec(ctx, `CREATE DATABASE `+name); err != nil {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || (pgErr.Code != "42P04" && pgErr.Code != "23505") {
			return err
		}
	}
	return nil
}

func Migrate(ctx context.Context, url, name string, m Migration) (int, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return 0, err
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return 0, err
	}
	defer pool.Close()
	return m(ctx, pool)
}

func Open(ctx context.Context, url, name string, migrations ...Migration) (*Store, error) {
	if err := Create(ctx, url, name); err != nil {
		return nil, err
	}
	s := &Store{url: url, name: name, migrations: migrations}
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
	if _, err := ledger.Migrate(ctx, pool); err != nil {
		pool.Close()
		return err
	}
	for _, m := range s.migrations {
		if _, err := m(ctx, pool); err != nil {
			pool.Close()
			return err
		}
	}
	s.Pool = pool
	return nil
}

func (s *Store) URL() string {
	return s.url
}

func (s *Store) Name() string {
	return s.name
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

var dbName = regexp.MustCompile(`^beerus_[a-z0-9_]+$`)

func List(ctx context.Context, url, prefix string) ([]string, error) {
	c, err := admin(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("cannot reach Postgres (%w)", err)
	}
	defer c.Close(ctx)
	rows, err := c.Query(ctx, `SELECT datname FROM pg_database WHERE datname LIKE $1 ORDER BY datname`, strings.ReplaceAll(prefix, "_", `\_`)+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func DropDatabases(ctx context.Context, url string, names []string) error {
	c, err := admin(ctx, url)
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	for _, n := range names {
		if !dbName.MatchString(n) {
			return fmt.Errorf("refusing to drop database %q", n)
		}
		if _, err := c.Exec(ctx, `DROP DATABASE IF EXISTS `+n+` WITH (FORCE)`); err != nil {
			return err
		}
	}
	return nil
}
