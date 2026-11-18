package connect

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evolutionlabs-core/beerus/internal/bank/banktest"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/install"
	"github.com/evolutionlabs-core/beerus/internal/state"
)

func testDatabaseURL(t *testing.T) string {
	t.Helper()
	t.Setenv(config.DatabaseURLEnv, "")
	url := install.DefaultLocal().DatabaseURL
	if u := os.Getenv("BEERUS_TEST_DATABASE_URL"); u != "" {
		url = u
	}
	conn, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Skipf("no Postgres for the ledger source test (%v); set BEERUS_TEST_DATABASE_URL", err)
	}
	conn.Close(context.Background())
	return url
}

func TestGenesisIsPostedAtTheProvisioningTimeOnce(t *testing.T) {
	url := testDatabaseURL(t)
	today := clock.Date{Y: 2026, M: 11, D: 3}
	local := install.DefaultLocal()
	local.DatabaseURL = url
	dir := t.TempDir()
	t.Cleanup(func() {
		if _, err := install.Reset(context.WithoutCancel(t.Context()), dir, url); err != nil {
			t.Errorf("drop databases: %v", err)
		}
	})
	if err := install.Init(dir, banktest.Spec("beerus"), local, today); err != nil {
		t.Fatal(err)
	}
	cfg, err := state.LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctl, err := state.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	clk := clock.New()
	before := clk.Now()
	b := &config.Bank{ID: "bank-g", Name: "Bank G", NIPCode: "999007", NUBANCode: "907", Database: config.DatabaseName(cfg.Dir, "bank-g")}
	postedAt := func(pool *pgxpool.Pool, key string) (time.Time, int) {
		t.Helper()
		var at time.Time
		var n int
		err := pool.QueryRow(t.Context(), `SELECT min(posted_at), count(*) FROM journal_entries WHERE idempotency_key = $1`, key).Scan(&at, &n)
		if err != nil {
			t.Fatal(err)
		}
		return at, n
	}
	var first time.Time
	for round := range 2 {
		ls, err := openLedgerSource(t.Context(), cfg, ctl, clk, b)
		if err != nil {
			t.Fatal(err)
		}
		got, n := postedAt(ls.Pool(), "genesis")
		if round == 0 {
			first = got
			if first.Before(before) || first.After(clk.Now()) {
				t.Fatalf("genesis posted at %v, want it between %v and now", first, before)
			}
			for !clk.Now().After(first) {
				runtime.Gosched()
			}
		}
		if !got.Equal(first) || n != 1 {
			t.Fatalf("round %d: genesis posted at %v, %d entries; want %v, 1", round, got, n, first)
		}
		if round == 1 {
			if err := ls.Drop(t.Context()); err != nil {
				t.Fatal(err)
			}
			continue
		}
		ls.Close()
	}
}
