package nibss

import (
	"context"
	"io/fs"
	"testing"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

const lastMigrationWithCertificates = 14

func TestMigrationRenamesCallerAndRewritesHTTPSURLs(t *testing.T) {
	ctx := context.Background()
	pool := scratchPool(t)
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, sub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, lastMigrationWithCertificates); err != nil {
		t.Fatal(err)
	}
	rows := []struct{ id, nip, nuban, url string }{
		{"bank-a", "000014", "014", "https://bank-a.localhost:7451"},
		{"bank-b", "000015", "015", "http://bank-b.localhost:7452"},
	}
	for _, r := range rows {
		if _, err := pool.Exec(ctx, `INSERT INTO institutions (id, name, nip_code, nuban_code, type, status, credit_url, certificate_identity) VALUES ($1, $1, $2, $3, 'commercial', 'active', $4, $1)`, r.id, r.nip, r.nuban, r.url); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := migrate(ctx, pool); err != nil || n == 0 {
		t.Fatalf("migrate: %d, %v", n, err)
	}
	want := map[string]string{"bank-a": "http://bank-a.localhost:7451", "bank-b": "http://bank-b.localhost:7452"}
	for id, url := range want {
		m, err := scanMember(pool.QueryRow(ctx, `SELECT `+memberCols+` FROM institutions WHERE id = $1`, id))
		if err != nil {
			t.Fatal(err)
		}
		if m.CreditURL != url || m.CallerID != id {
			t.Errorf("%s: credit_url %q and caller_id %q, want %q and %q", id, m.CreditURL, m.CallerID, url, id)
		}
	}
}
