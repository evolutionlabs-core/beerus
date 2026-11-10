package nibss

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

var rolledBack = []string{"settlement_sessions", "nip_sessions"}

type rowSource interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func copyTable(ctx context.Context, from rowSource, to pgx.Tx, table string) error {
	rows, err := from.Query(ctx, `SELECT * FROM `+table)
	if err != nil {
		return fmt.Errorf("read %s: %w", table, err)
	}
	defer rows.Close()
	cols := []string{}
	for _, f := range rows.FieldDescriptions() {
		cols = append(cols, f.Name)
	}
	if _, err := to.CopyFrom(ctx, pgx.Identifier{table}, cols, rows); err != nil {
		return fmt.Errorf("copy %s: %w", table, err)
	}
	return nil
}

func (s *Store) Snapshot(ctx context.Context, url, name string) error {
	snap, err := Open(ctx, url, name)
	if err != nil {
		return fmt.Errorf("open NIBSS snapshot %s: %w", name, err)
	}
	defer snap.Close()
	var used bool
	if err := snap.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM institutions)`).Scan(&used); err != nil {
		return fmt.Errorf("check NIBSS snapshot %s: %w", name, err)
	}
	if used {
		return fmt.Errorf("NIBSS snapshot %s already holds data", name)
	}
	src, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin NIBSS snapshot read: %w", err)
	}
	defer src.Rollback(ctx)
	tx, err := snap.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin NIBSS snapshot write: %w", err)
	}
	defer tx.Rollback(ctx)
	for _, t := range append([]string{"institutions"}, rolledBack...) {
		if err := copyTable(ctx, src, tx, t); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit NIBSS snapshot %s: %w", name, err)
	}
	return nil
}

func (s *Store) Restore(ctx context.Context, url, name string) error {
	ok, err := databaseExists(ctx, url, name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("NIBSS snapshot %s does not exist", name)
	}
	snap, err := Open(ctx, url, name)
	if err != nil {
		return fmt.Errorf("open NIBSS snapshot %s: %w", name, err)
	}
	defer snap.Close()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin NIBSS restore: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM nip_sessions`); err != nil {
		return fmt.Errorf("clear NIP sessions: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM settlement_sessions`); err != nil {
		return fmt.Errorf("clear settlement sessions: %w", err)
	}
	for _, t := range rolledBack {
		if err := copyTable(ctx, snap.pool, tx, t); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit NIBSS restore: %w", err)
	}
	return nil
}

func databaseExists(ctx context.Context, url, name string) (bool, error) {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return false, fmt.Errorf("parse NIBSS database url: %w", err)
	}
	cfg.Database = "postgres"
	c, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return false, fmt.Errorf("cannot reach Postgres for NIBSS: %w", err)
	}
	defer c.Close(ctx)
	var ok bool
	if err := c.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&ok); err != nil {
		return false, fmt.Errorf("look up NIBSS database %s: %w", name, err)
	}
	return ok, nil
}
