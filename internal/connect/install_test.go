package connect

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/evolutionlabs-core/beerus/internal/bank/banktest"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func countMigrations(t *testing.T, dir string) int {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	return len(files)
}

func installURL(t *testing.T) string {
	t.Helper()
	t.Setenv(config.DatabaseURLEnv, "")
	url := DefaultLocal().DatabaseURL
	if u := os.Getenv("BEERUS_TEST_DATABASE_URL"); u != "" {
		url = u
	}
	conn, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Skipf("no Postgres for install tests (%v); set BEERUS_TEST_DATABASE_URL", err)
	}
	conn.Close(context.Background())
	return url
}

func runInstall(ctx context.Context, dir, url string, after func(InstallStep)) ([]string, error) {
	today := clock.Date{Y: 2026, M: 11, D: 3}
	local := DefaultLocal()
	local.DatabaseURL = url
	var lines []string
	err := Install(ctx, dir, banktest.Spec("beerus", today), local, today, func(s InstallStep) {
		mark := "ok"
		if s.Err != nil {
			mark = "failed"
		}
		lines = append(lines, fmt.Sprintf("%s|%s|%s", s.Name, s.Detail, mark))
		if after != nil {
			after(s)
		}
	})
	return lines, err
}

func TestInstall(t *testing.T) {
	url := installURL(t)
	ledgerN := countMigrations(t, "../bank/ledger/migrations")
	nibssN := countMigrations(t, "../nibss/migrations")
	migrations := func(n int) string { return fmt.Sprintf("%d migrations", n) }

	freshLines := func(dir string) []string {
		return []string{
			"Checking Postgres||ok",
			"Writing config||ok",
			"Creating CBN database||ok",
			"Migrating CBN ledger|" + migrations(ledgerN) + "|ok",
			"Opening CBN settlement ledger||ok",
			"Creating NIBSS database||ok",
			"Migrating NIBSS schema|" + migrations(nibssN) + "|ok",
		}
	}

	cases := []struct {
		name        string
		cancelAfter string
		wantFirst   func(dir string) []string
		wantFirstEr string
		wantResume  func(dir string) []string
	}{
		{
			name:      "fresh install, then a rerun is refused",
			wantFirst: freshLines,
		},
		{
			name:        "resume after Creating CBN database fails",
			cancelAfter: "Writing config",
			wantFirst: func(dir string) []string {
				return []string{"Checking Postgres||ok", "Writing config||ok", "Creating CBN database||failed"}
			},
			wantFirstEr: "context canceled",
			wantResume: func(dir string) []string {
				lines := freshLines(dir)
				lines[1] = "Writing config|already written|ok"
				return lines
			},
		},
		{
			name:        "resume skips the steps that are recorded as done",
			cancelAfter: "Migrating CBN ledger",
			wantFirst: func(dir string) []string {
				return []string{
					"Checking Postgres||ok",
					"Writing config||ok",
					"Creating CBN database||ok",
					"Migrating CBN ledger|" + migrations(ledgerN) + "|ok",
					"Opening CBN settlement ledger||failed",
				}
			},
			wantFirstEr: "context canceled",
			wantResume: func(dir string) []string {
				lines := freshLines(dir)
				lines[1] = "Writing config|already written|ok"
				lines[3] = "Migrating CBN ledger|no new migrations|ok"
				return lines
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Cleanup(func() {
				if _, err := Reset(context.Background(), dir, url); err != nil {
					t.Errorf("drop databases: %v", err)
				}
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cut := func(s InstallStep) {
				if c.cancelAfter != "" && strings.HasPrefix(s.Name, c.cancelAfter) {
					cancel()
				}
			}
			got, err := runInstall(ctx, dir, url, cut)
			if want := c.wantFirst(dir); !slices.Equal(got, want) {
				t.Fatalf("first run:\n got %q\nwant %q", got, want)
			}
			switch {
			case c.wantFirstEr == "" && err != nil:
				t.Fatalf("first run: %v", err)
			case c.wantFirstEr != "" && (err == nil || !errors.Is(err, context.Canceled)):
				t.Fatalf("first run error: %v", err)
			}
			if c.wantFirstEr != "" {
				if err := CheckStart(dir); err == nil || err.Error() != "the setup in "+dir+" is not finished. Run beerus init again to finish it." {
					t.Fatalf("up before the resume: %v", err)
				}
				got, err = runInstall(context.Background(), dir, url, nil)
				if err != nil {
					t.Fatalf("resume: %v", err)
				}
				if want := c.wantResume(dir); !slices.Equal(got, want) {
					t.Fatalf("resume:\n got %q\nwant %q", got, want)
				}
			}
			st, err := setup.LoadStatus(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{setup.SystemCBN, setup.SystemNIBSS} {
				if r := findResource(&st, setup.KindSystem, id); r == nil || r.Phase != setup.PhaseActive {
					t.Errorf("%s after install: %+v", id, r)
				}
			}
			got, err = runInstall(context.Background(), dir, url, nil)
			if want := dir + " is already set up. Run beerus up to start it."; err == nil || err.Error() != want || len(got) != 0 {
				t.Fatalf("rerun: %q %v, want %q", got, err, want)
			}
			if err := CheckStart(dir); err != nil {
				t.Fatalf("up after install: %v", err)
			}
		})
	}

	t.Run("unreachable Postgres writes nothing", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "setup")
		got, err := runInstall(context.Background(), dir, "postgres://localhost:1/postgres", nil)
		want := "cannot reach Postgres at localhost:1. Postgres is not installed or not running."
		if err == nil || err.Error() != want {
			t.Fatalf("error: %v, want %q", err, want)
		}
		if !slices.Equal(got, []string{"Checking Postgres||failed"}) {
			t.Fatalf("steps: %q", got)
		}
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("a failed check wrote %s: %v", dir, err)
		}
		if err := CheckStart(dir); err == nil || err.Error() != "no Beerus setup in "+dir+". Run beerus init first." {
			t.Fatalf("up on nothing: %v", err)
		}
	})

	t.Run("opening the CBN ledger activates CBN", func(t *testing.T) {
		today := clock.Date{Y: 2026, M: 11, D: 3}
		local := DefaultLocal()
		local.DatabaseURL = url
		dir := t.TempDir()
		t.Cleanup(func() {
			if _, err := Reset(context.Background(), dir, url); err != nil {
				t.Errorf("drop databases: %v", err)
			}
		})
		if err := Init(dir, banktest.Spec("beerus", today), local, today); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		ctl, err := openStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		r, err := openRTGS(context.Background(), cfg, ctl, cfg.Start)
		if err != nil {
			t.Fatal(err)
		}
		r.Close()
		st, err := ctl.status()
		if err != nil {
			t.Fatal(err)
		}
		if got := systemPhase(st, setup.SystemCBN); got != setup.PhaseActive {
			t.Fatalf("CBN phase after openRTGS: %q", got)
		}
	})
}
