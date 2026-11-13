package openbanking

import (
	"context"
	"embed"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

func Migrate(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return 0, err
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, sub, goose.WithTableName("goose_openbanking"))
	if err != nil {
		return 0, err
	}
	results, err := p.Up(ctx)
	return len(results), err
}
