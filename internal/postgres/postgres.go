package postgres

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"regexp"
	"strings"
)

func Admin(ctx context.Context, url string) (*pgx.Conn, error) {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.Database = "postgres"
	return pgx.ConnectConfig(ctx, cfg)
}

var dbName = regexp.MustCompile(`^beerus_[a-z0-9_]+$`)

func List(ctx context.Context, url, prefix string) ([]string, error) {
	c, err := Admin(ctx, url)
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
	c, err := Admin(ctx, url)
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
