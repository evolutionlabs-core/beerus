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

func testStore(t testing.TB) *Store {
	t.Helper()
	s, err := newStore(context.Background(), scratchPool(t))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func register(t testing.TB, s *Store, id, name, code, nuban string) {
	t.Helper()
	if err := s.Register(context.Background(), Institution{ID: id, Name: name, NIPCode: code, NUBANCode: nuban, Type: "commercial"}); err != nil {
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
	_, err := Open(context.Background(), testBaseURL(), "nibss")
	wantErr(t, err, `database name "nibss" must look like beerus_something`)
}

func TestDirectory(t *testing.T) {
	ctx := context.Background()
	pool := scratchPool(t)
	s, err := newStore(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := s.Directory(ctx)
	if err != nil || len(dir) != 0 {
		t.Fatalf("new directory: %+v, %v", dir, err)
	}
	register(t, s, "bank-b", "Bank B", "999002", "902")
	register(t, s, "bank-a", "Bank A", "999001", "901")
	again, err := newStore(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	dir, err = again.Directory(ctx)
	want := []Institution{
		{ID: "bank-a", Name: "Bank A", NIPCode: "999001", NUBANCode: "901", Type: "commercial", Status: StatusActive},
		{ID: "bank-b", Name: "Bank B", NIPCode: "999002", NUBANCode: "902", Type: "commercial", Status: StatusActive},
	}
	if err != nil || len(dir) != len(want) {
		t.Fatalf("directory: %+v, %v", dir, err)
	}
	for i := range want {
		if dir[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, dir[i], want[i])
		}
	}
}

func TestInstitutionUnknown(t *testing.T) {
	_, err := testStore(t).Institution(context.Background(), "123456")
	if !errors.Is(err, ErrUnknownInstitution) {
		t.Fatalf("got %v, want ErrUnknownInstitution", err)
	}
	wantErr(t, err, "bank code 123456 is not in the NIBSS directory")
}

func TestSetStatus(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	register(t, s, "bank-c", "Bank C", "999003", "903")
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
	if !errors.Is(err, ErrUnknownInstitution) {
		t.Fatalf("got %v, want ErrUnknownInstitution", err)
	}
	wantErr(t, err, "bank id bank-x is not in the NIBSS directory")
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
	for i := range 2 {
		s, err := Open(ctx, testBaseURL(), name)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			register(t, s, "bank-a", "Bank A", "999001", "901")
		}
		dir, err := s.Directory(ctx)
		s.Close()
		if err != nil || len(dir) != 1 || dir[0].ID != "bank-a" {
			t.Fatalf("open %d: directory %+v, %v", i, dir, err)
		}
	}
}
