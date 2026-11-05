package nibss

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/evolutionlabs-core/beerus/internal/countrypack"
)

//go:embed migrations/*.sql
var migrations embed.FS

var validName = regexp.MustCompile(`^beerus_[a-z0-9_]{1,50}$`)

const maxConns = 8

var (
	ErrAskBank = errors.New("the account name comes from the receiving bank")

	errTransition = errors.New("status change not allowed")
)

var transitions = map[string][]string{
	StatusActive:      {StatusSuspended, StatusTerminating},
	StatusSuspended:   {StatusActive, StatusTerminating},
	StatusTerminating: {StatusRemoved},
	StatusRemoved:     {},
}

type Store struct {
	pool  *pgxpool.Pool
	phone countrypack.Phone
}

var _ Switch = (*Store)(nil)

func Open(ctx context.Context, url, name string) (*Store, error) {
	if !validName.MatchString(name) {
		return nil, fmt.Errorf("database name %q must look like beerus_something", name)
	}
	if err := createDatabase(ctx, url, name); err != nil {
		return nil, err
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse NIBSS database url: %w", err)
	}
	cfg.ConnConfig.Database = name
	cfg.MaxConns = maxConns
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open NIBSS database: %w", err)
	}
	s, err := newStore(ctx, pool)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

func createDatabase(ctx context.Context, url, name string) error {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return fmt.Errorf("parse NIBSS database url: %w", err)
	}
	cfg.Database = "postgres"
	c, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("cannot reach Postgres for NIBSS (%w); start Postgres or set its database url", err)
	}
	defer c.Close(ctx)
	var ok bool
	if err := c.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&ok); err != nil {
		return fmt.Errorf("look up NIBSS database: %w", err)
	}
	if ok {
		return nil
	}
	if _, err := c.Exec(ctx, `CREATE DATABASE `+name); err != nil {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || (pgErr.Code != "42P04" && pgErr.Code != "23505") {
			return fmt.Errorf("create NIBSS database: %w", err)
		}
	}
	return nil
}

func newStore(ctx context.Context, pool *pgxpool.Pool) (*Store, error) {
	p, err := countrypack.Nigeria()
	if err != nil {
		return nil, fmt.Errorf("load country pack: %w", err)
	}
	if err := migrate(ctx, pool); err != nil {
		return nil, err
	}
	return &Store{pool: pool, phone: p.Phone}, nil
}

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("read NIBSS migrations: %w", err)
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, sub)
	if err != nil {
		return fmt.Errorf("load NIBSS migrations: %w", err)
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("run NIBSS migrations: %w", err)
	}
	return nil
}

func (s *Store) Close() {
	s.pool.Close()
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

const institutionCols = `id, name, nip_code, nuban_code, type, status`

func scanInstitution(row pgx.Row) (Institution, error) {
	var in Institution
	err := row.Scan(&in.ID, &in.Name, &in.NIPCode, &in.NUBANCode, &in.Type, &in.Status)
	return in, err
}

func (s *Store) Directory(ctx context.Context) ([]Institution, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+institutionCols+` FROM institutions ORDER BY nip_code`)
	if err != nil {
		return nil, fmt.Errorf("list institutions: %w", err)
	}
	defer rows.Close()
	out := []Institution{}
	for rows.Next() {
		in, err := scanInstitution(rows)
		if err != nil {
			return nil, fmt.Errorf("read institution: %w", err)
		}
		out = append(out, in)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list institutions: %w", err)
	}
	return out, nil
}

func (s *Store) Institution(ctx context.Context, code string) (Institution, error) {
	return institution(ctx, s.pool, code, "")
}

func institution(ctx context.Context, q querier, code, lock string) (Institution, error) {
	in, err := scanInstitution(q.QueryRow(ctx, `SELECT `+institutionCols+` FROM institutions WHERE nip_code = $1 `+lock, code))
	if errors.Is(err, pgx.ErrNoRows) {
		return Institution{}, fmt.Errorf("bank code %s is %w", code, ErrUnknownInstitution)
	}
	if err != nil {
		return Institution{}, fmt.Errorf("look up institution %s: %w", code, err)
	}
	return in, nil
}

func (s *Store) SetStatus(ctx context.Context, id, status string) error {
	if _, ok := transitions[status]; !ok {
		return fmt.Errorf("unknown institution status %q", status)
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var cur string
		err := tx.QueryRow(ctx, `SELECT status FROM institutions WHERE id = $1 FOR UPDATE`, id).Scan(&cur)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("bank id %s is %w", id, ErrUnknownInstitution)
		}
		if err != nil {
			return fmt.Errorf("look up institution %s: %w", id, err)
		}
		if cur == status {
			return nil
		}
		if !slices.Contains(transitions[cur], status) {
			return fmt.Errorf("institution %s is %s and cannot become %s: %w", id, cur, status, errTransition)
		}
		if _, err := tx.Exec(ctx, `UPDATE institutions SET status = $2, updated_at = now() WHERE id = $1`, id, status); err != nil {
			return fmt.Errorf("set institution %s status: %w", id, err)
		}
		return nil
	})
}
