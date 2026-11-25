package store_test

import (
	"context"
	"github.com/evolutionlabs-core/beerus/internal/postgres"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/banktest"
	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

func TestGenesisIsPostedAtTheProvisioningTimeOnce(t *testing.T) {
	url := os.Getenv("BEERUS_TEST_DATABASE_URL")
	if url == "" {
		url = "host=/tmp dbname=postgres"
	}
	conn, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Skipf("no Postgres for store tests (%v); set BEERUS_TEST_DATABASE_URL", err)
	}
	conn.Close(context.Background())
	name := config.DatabaseName(t.TempDir(), "bank-g")
	t.Cleanup(func() {
		if err := postgres.DropDatabases(context.WithoutCancel(t.Context()), url, []string{name}); err != nil {
			t.Errorf("drop database: %v", err)
		}
	})
	first := time.Date(2026, time.November, 12, 10, 30, 15, 0, clock.Lagos)
	second := first.Add(48 * time.Hour)
	profile := bank.Profile{ID: "bank-g", Name: "Bank G", NIPCode: "999007", NUBANCode: "907"}
	fees := banktest.Fees(t)
	for _, at := range []time.Time{first, second} {
		books, err := store.OpenBooks(t.Context(), url, name, profile, fees, at, openbanking.Migrate)
		chk(t, err)
		var got time.Time
		var n int
		err = books.Pool().QueryRow(t.Context(), `SELECT min(posted_at), count(*) FROM journal_entries WHERE idempotency_key = $1`, "genesis").Scan(&got, &n)
		books.Close()
		chk(t, err)
		if !got.Equal(first) || n != 1 {
			t.Fatalf("bank genesis opened at %v: posted at %v, %d entries; want %v, 1", at, got, n, first)
		}
	}
}

func chk(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
