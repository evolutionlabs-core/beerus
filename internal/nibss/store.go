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
	"github.com/pressly/goose/v3/lock"

	"github.com/evolutionlabs-core/beerus/internal/countrypack"
	"github.com/evolutionlabs-core/beerus/internal/nip"
)

//go:embed migrations/*.sql
var migrations embed.FS

var validName = regexp.MustCompile(`^beerus_[a-z0-9_]{1,50}$`)

const maxConns = 8

var ErrTransition = errors.New("status change not allowed")

var transitions = map[string][]string{
	nip.StatusActive:      {nip.StatusSuspended, nip.StatusTerminating},
	nip.StatusSuspended:   {nip.StatusActive, nip.StatusTerminating},
	nip.StatusTerminating: {nip.StatusRemoved},
	nip.StatusRemoved:     {},
}

func Transitions(from string) []string {
	return slices.Clone(transitions[from])
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
	if err := Create(ctx, url, name); err != nil {
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

func Create(ctx context.Context, url, name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("database name %q must look like beerus_something", name)
	}
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
	if _, err := migrate(ctx, pool); err != nil {
		return nil, err
	}
	return &Store{pool: pool, phone: p.Phone}, nil
}

func Migrate(ctx context.Context, url, name string) (int, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return 0, fmt.Errorf("parse NIBSS database url: %w", err)
	}
	cfg.ConnConfig.Database = name
	cfg.MaxConns = maxConns
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return 0, fmt.Errorf("open NIBSS database: %w", err)
	}
	defer pool.Close()
	return migrate(ctx, pool)
}

func migrate(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return 0, fmt.Errorf("read NIBSS migrations: %w", err)
	}
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return 0, fmt.Errorf("make NIBSS migration lock: %w", err)
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, sub, goose.WithSessionLocker(locker))
	if err != nil {
		return 0, fmt.Errorf("load NIBSS migrations: %w", err)
	}
	results, err := p.Up(ctx)
	if err != nil {
		return 0, fmt.Errorf("run NIBSS migrations: %w", err)
	}
	return len(results), nil
}

func (s *Store) Close() {
	s.pool.Close()
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

const institutionCols = `id, name, nip_code, nuban_code, type, status`

const memberCols = institutionCols + `, credit_url, certificate_identity`

func scanMember(row pgx.Row) (Member, error) {
	var m Member
	err := row.Scan(&m.ID, &m.Name, &m.NIPCode, &m.NUBANCode, &m.Type, &m.Status, &m.CreditURL, &m.CertificateIdentity)
	return m, err
}

func (s *Store) Member(ctx context.Context, id string) (Member, error) {
	m, err := scanMember(s.pool.QueryRow(ctx, `SELECT `+memberCols+` FROM institutions WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Member{}, fmt.Errorf("bank id %s is %w", id, nip.ErrUnknownInstitution)
	}
	if err != nil {
		return Member{}, fmt.Errorf("look up institution %s: %w", id, err)
	}
	return m, nil
}

func (s *Store) MemberAt(ctx context.Context, code string) (Member, error) {
	m, err := scanMember(s.pool.QueryRow(ctx, `SELECT `+memberCols+` FROM institutions WHERE nip_code = $1`, code))
	if errors.Is(err, pgx.ErrNoRows) {
		return Member{}, fmt.Errorf("bank code %s is %w", code, nip.ErrUnknownInstitution)
	}
	if err != nil {
		return Member{}, fmt.Errorf("look up institution %s: %w", code, err)
	}
	return m, nil
}

func (s *Store) Admits(ctx context.Context, identity string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM institutions WHERE certificate_identity = $1 AND status <> 'removed')`, identity).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("look up certificate identity %s: %w", identity, err)
	}
	return ok, nil
}

func scanInstitution(row pgx.Row) (nip.Institution, error) {
	var in nip.Institution
	err := row.Scan(&in.ID, &in.Name, &in.NIPCode, &in.NUBANCode, &in.Type, &in.Status)
	return in, err
}

func (s *Store) Directory(ctx context.Context) ([]nip.Institution, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+institutionCols+` FROM institutions ORDER BY nip_code`)
	if err != nil {
		return nil, fmt.Errorf("list institutions: %w", err)
	}
	defer rows.Close()
	out := []nip.Institution{}
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

func (s *Store) Institution(ctx context.Context, code string) (nip.Institution, error) {
	return institution(ctx, s.pool, code, "")
}

func institution(ctx context.Context, q querier, code, lock string) (nip.Institution, error) {
	in, err := scanInstitution(q.QueryRow(ctx, `SELECT `+institutionCols+` FROM institutions WHERE nip_code = $1 `+lock, code))
	if errors.Is(err, pgx.ErrNoRows) {
		return nip.Institution{}, fmt.Errorf("bank code %s is %w", code, nip.ErrUnknownInstitution)
	}
	if err != nil {
		return nip.Institution{}, fmt.Errorf("look up institution %s: %w", code, err)
	}
	return in, nil
}

func (s *Store) SetStatus(ctx context.Context, id, status string) error {
	if _, ok := transitions[status]; !ok {
		return invalid("unknown institution status %q", status)
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var cur string
		err := tx.QueryRow(ctx, `SELECT status FROM institutions WHERE id = $1 FOR UPDATE`, id).Scan(&cur)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("bank id %s is %w", id, nip.ErrUnknownInstitution)
		}
		if err != nil {
			return fmt.Errorf("look up institution %s: %w", id, err)
		}
		if cur == status {
			return nil
		}
		if !slices.Contains(transitions[cur], status) {
			return fmt.Errorf("institution %s is %s and cannot become %s: %w", id, cur, status, ErrTransition)
		}
		if _, err := tx.Exec(ctx, `UPDATE institutions SET status = $2, updated_at = now() WHERE id = $1`, id, status); err != nil {
			return fmt.Errorf("set institution %s status: %w", id, err)
		}
		return nil
	})
}
