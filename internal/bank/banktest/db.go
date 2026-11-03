package banktest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
)

const database = "beerus_test"

func baseURL() string {
	if u := os.Getenv("BEERUS_TEST_DATABASE_URL"); u != "" {
		return u
	}
	return "host=/tmp dbname=postgres"
}

func ensureDatabase(ctx context.Context, t testing.TB) {
	conn, err := pgx.Connect(ctx, baseURL())
	if err != nil {
		t.Skipf("no Postgres for ledger tests (%v); set BEERUS_TEST_DATABASE_URL", err)
	}
	defer conn.Close(ctx)
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, database).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		_, err := conn.Exec(ctx, `CREATE DATABASE `+database)
		var pgErr *pgconn.PgError
		if err != nil && !(errors.As(err, &pgErr) && (pgErr.Code == "42P04" || pgErr.Code == "23505")) {
			t.Fatal(err)
		}
	}
}

func testConfig(t testing.TB) *pgx.ConnConfig {
	cfg, err := pgx.ParseConfig(baseURL())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Database = database
	return cfg
}

func Schema(t testing.TB) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	ensureDatabase(ctx, t)
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "t_" + hex.EncodeToString(b)
	admin, err := pgx.ConnectConfig(ctx, testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	admin.Close(ctx)
	cfg, err := pgxpool.ParseConfig(baseURL())
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = database
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 16
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		c, err := pgx.ConnectConfig(context.Background(), testConfig(t))
		if err == nil {
			_, _ = c.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
			c.Close(context.Background())
		}
	})
	if err := ledger.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}
