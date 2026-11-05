package nibss

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

	"github.com/evolutionlabs-core/beerus/internal/countrypack"
)

const testDatabase = "beerus_test"

func testBaseURL() string {
	if u := os.Getenv("BEERUS_TEST_DATABASE_URL"); u != "" {
		return u
	}
	return "host=/tmp dbname=postgres"
}

func testConn(ctx context.Context, t testing.TB, db string) *pgx.Conn {
	cfg, err := pgx.ParseConfig(testBaseURL())
	if err != nil {
		t.Fatal(err)
	}
	if db != "" {
		cfg.Database = db
	}
	c, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Skipf("no Postgres for NIBSS tests (%v); set BEERUS_TEST_DATABASE_URL", err)
	}
	return c
}

func scratchPool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	c := testConn(ctx, t, "")
	var exists bool
	if err := c.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, testDatabase).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		_, err := c.Exec(ctx, `CREATE DATABASE `+testDatabase)
		var pgErr *pgconn.PgError
		if err != nil && !(errors.As(err, &pgErr) && (pgErr.Code == "42P04" || pgErr.Code == "23505")) {
			t.Fatal(err)
		}
	}
	c.Close(ctx)
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "n_" + hex.EncodeToString(b)
	admin := testConn(ctx, t, testDatabase)
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	admin.Close(ctx)
	cfg, err := pgxpool.ParseConfig(testBaseURL())
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = testDatabase
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 16
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		c := testConn(context.Background(), t, testDatabase)
		_, _ = c.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		c.Close(context.Background())
	})
	return pool
}

func packOutside(t testing.TB) []Institution {
	t.Helper()
	p, err := countrypack.Nigeria()
	if err != nil {
		t.Fatal(err)
	}
	return Outside(p)
}

func testStore(t testing.TB) *Store {
	t.Helper()
	s, err := newStore(context.Background(), scratchPool(t), packOutside(t))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func addMember(t testing.TB, s *Store, id, name, code, nuban, status string) {
	t.Helper()
	_, err := s.pool.Exec(context.Background(), `
INSERT INTO institutions (id, name, nip_code, nuban_code, type, outside, status) VALUES ($1, $2, $3, $4, 'commercial', false, $5)
ON CONFLICT (nip_code) DO UPDATE SET id = EXCLUDED.id, name = EXCLUDED.name, nuban_code = EXCLUDED.nuban_code, outside = false, status = EXCLUDED.status`,
		id, name, code, nuban, status)
	if err != nil {
		t.Fatal(err)
	}
}

func wantErr(t *testing.T, err error, msg string) {
	t.Helper()
	if err == nil || err.Error() != msg {
		t.Fatalf("got error %v, want %q", err, msg)
	}
}

func TestOpenRejectsBadName(t *testing.T) {
	_, err := Open(context.Background(), testBaseURL(), "nibss", nil)
	wantErr(t, err, `database name "nibss" must look like beerus_something`)
}

func TestOutsideLoadedFromPack(t *testing.T) {
	ctx := context.Background()
	pool := scratchPool(t)
	s, err := newStore(ctx, pool, packOutside(t))
	if err != nil {
		t.Fatal(err)
	}
	dir, err := s.Directory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []Institution{
		{ID: "999001", Name: "Bank A", NIPCode: "999001", NUBANCode: "999001", Type: "commercial", Outside: true, Status: StatusActive},
		{ID: "999002", Name: "Bank B", NIPCode: "999002", NUBANCode: "999002", Type: "digital", Outside: true, Status: StatusActive},
		{ID: "999003", Name: "Bank C", NIPCode: "999003", NUBANCode: "999003", Type: "commercial", Outside: true, Status: StatusActive},
		{ID: "999004", Name: "Bank D Microfinance", NIPCode: "999004", NUBANCode: "999004", Type: "microfinance", Outside: true, Status: StatusActive},
	}
	if len(dir) != len(want) {
		t.Fatalf("directory has %d rows, want %d", len(dir), len(want))
	}
	for i := range want {
		if dir[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, dir[i], want[i])
		}
	}
	addMember(t, s, "bank-a", "Bank A", "999001", "901", StatusActive)
	again, err := newStore(ctx, pool, packOutside(t))
	if err != nil {
		t.Fatal(err)
	}
	in, err := again.Institution(ctx, "999001")
	if err != nil {
		t.Fatal(err)
	}
	if in.ID != "bank-a" || in.Outside || in.NUBANCode != "901" {
		t.Errorf("reopening overwrote the member bank: %+v", in)
	}
	dir, _ = again.Directory(ctx)
	if len(dir) != 4 {
		t.Errorf("reopening left %d rows, want 4", len(dir))
	}
}

func TestOutsideMustBeComplete(t *testing.T) {
	_, err := newStore(context.Background(), scratchPool(t), []Institution{{ID: "999009", Name: "Bank Z", NIPCode: "999009", NUBANCode: "999009", Type: "commercial"}})
	wantErr(t, err, `outside bank "Bank Z" needs outside set, an id, name, codes and a type`)
	_, err = newStore(context.Background(), scratchPool(t), []Institution{{ID: "999009", Name: "Bank Z", NIPCode: "999009", NUBANCode: "999009", Type: "commercial", Outside: true, Status: StatusSuspended}})
	wantErr(t, err, "outside bank Bank Z must be active, not suspended")
}

func TestInstitutionUnknown(t *testing.T) {
	_, err := testStore(t).Institution(context.Background(), "123456")
	if !errors.Is(err, ErrNoRecord) {
		t.Fatalf("got %v, want ErrNoRecord", err)
	}
	wantErr(t, err, "no institution has code 123456: 25 no record")
}

func TestSetStatus(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	addMember(t, s, "bank-c", "Bank C", "999003", "903", StatusActive)
	steps := []struct {
		to   string
		want string
	}{
		{StatusSuspended, ""},
		{StatusSuspended, ""},
		{StatusActive, ""},
		{StatusRemoved, "institution bank-c is active and cannot become removed: status change not allowed"},
		{StatusTerminating, ""},
		{StatusActive, "institution bank-c is terminating and cannot become active: status change not allowed"},
		{StatusRemoved, ""},
		{StatusActive, "institution bank-c is removed and cannot become active: status change not allowed"},
	}
	for i, st := range steps {
		err := s.SetStatus(ctx, "bank-c", st.to)
		if st.want == "" && err != nil {
			t.Fatalf("step %d to %s: %v", i, st.to, err)
		}
		if st.want != "" {
			wantErr(t, err, st.want)
		}
	}
	in, _ := s.Institution(ctx, "999003")
	if in.Status != StatusRemoved {
		t.Errorf("status %s, want removed", in.Status)
	}
	wantErr(t, s.SetStatus(ctx, "bank-c", "closed"), `unknown institution status "closed"`)
	err := s.SetStatus(ctx, "bank-x", StatusActive)
	if !errors.Is(err, ErrNoRecord) {
		t.Fatalf("got %v, want ErrNoRecord", err)
	}
}

func TestOpenCreatesDatabase(t *testing.T) {
	ctx := context.Background()
	testConn(ctx, t, "").Close(ctx)
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	name := "beerus_nibss_t" + hex.EncodeToString(b)
	t.Cleanup(func() {
		c := testConn(context.Background(), t, "")
		_, _ = c.Exec(context.Background(), `DROP DATABASE IF EXISTS `+name)
		c.Close(context.Background())
	})
	for range 2 {
		s, err := Open(ctx, testBaseURL(), name, packOutside(t))
		if err != nil {
			t.Fatal(err)
		}
		dir, err := s.Directory(ctx)
		s.Close()
		if err != nil || len(dir) != 4 {
			t.Fatalf("directory %d rows, %v; want 4", len(dir), err)
		}
	}
}
