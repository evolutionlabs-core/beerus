package install

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
	"github.com/evolutionlabs-core/beerus/internal/postgres"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/state"
)

func installURL(t *testing.T) string {
	t.Helper()
	t.Setenv(config.DatabaseURLEnv, "")
	url := setup.DefaultLocal().DatabaseURL
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
	local := setup.DefaultLocal()
	local.DatabaseURL = url
	var lines []string
	err := Install(ctx, dir, banktest.Spec("beerus"), local, clock.Date{Y: 2026, M: 11, D: 3}, func(s InstallStep) {
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
	fresh := []string{"Checking Postgres||ok", "Writing config||ok", "Reserving CBN and NIBSS database names||ok", "Preparing CBN ports||ok"}
	resumed := slices.Clone(fresh)
	resumed[1] = "Writing config|already written|ok"
	cut := slices.Clone(fresh)
	cut[3] = "Preparing CBN ports||failed"
	for _, c := range []struct {
		name  string
		first []string
		after string
		again []string
	}{
		{"fresh install, then a rerun is refused", fresh, "", nil},
		{"resume after Preparing CBN ports fails", cut, "Reserving", resumed},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Cleanup(func() {
				if _, err := Reset(context.Background(), dir, url); err != nil {
					t.Errorf("drop databases: %v", err)
				}
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			got, err := runInstall(ctx, dir, url, func(s InstallStep) {
				if c.after != "" && strings.HasPrefix(s.Name, c.after) {
					cancel()
				}
			})
			if !slices.Equal(got, c.first) || (c.after == "") != (err == nil) || (c.after != "" && !errors.Is(err, context.Canceled)) {
				t.Fatalf("first run:\n got %q, %v\nwant %q", got, err, c.first)
			}
			if c.after != "" {
				if err := CheckStart(dir); err == nil || err.Error() != "the setup in "+dir+" is not finished. Run beerus init again to finish it." {
					t.Fatalf("up before the resume: %v", err)
				}
				if got, err = runInstall(context.Background(), dir, url, nil); err != nil || !slices.Equal(got, c.again) {
					t.Fatalf("resume:\n got %q, %v\nwant %q", got, err, c.again)
				}
			}
			got, err = runInstall(context.Background(), dir, url, nil)
			if want := dir + " is already set up. Run beerus up to start it."; err == nil || err.Error() != want || len(got) != 0 {
				t.Fatalf("rerun: %q %v, want %q", got, err, want)
			}
			if err := CheckStart(dir); err != nil {
				t.Fatalf("up after install: %v", err)
			}
			st, err := setup.LoadStatus(dir)
			if err != nil {
				t.Fatal(err)
			}
			made, err := postgres.List(t.Context(), url, config.DatabaseName(dir, ""))
			if err != nil {
				t.Fatal(err)
			}
			claims := state.Owned(st, state.OwnedDatabase)
			for _, id := range []string{setup.SystemCBN, setup.SystemNIBSS} {
				name := config.DatabaseName(dir, id)
				if r := setup.FindResource(&st, setup.KindSystem, id); r == nil || r.Phase != config.PhaseActive || claims[name] != setup.KindSystem+"/"+id || slices.Contains(made, name) {
					t.Errorf("%s after install: %+v, claims %v, made %v", id, r, claims, made)
				}
			}
		})
	}

	t.Run("unreachable Postgres writes nothing", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "setup")
		got, err := runInstall(context.Background(), dir, "postgres://localhost:1/postgres", nil)
		want := "cannot reach Postgres at localhost:1. Postgres is not installed or not running."
		if err == nil || err.Error() != want || !slices.Equal(got, []string{"Checking Postgres||failed"}) {
			t.Fatalf("steps %q, error %v, want %q", got, err, want)
		}
		if err := CheckStart(dir); err == nil || err.Error() != "no Beerus setup in "+dir+". Run beerus init first." {
			t.Fatalf("up on nothing: %v", err)
		}
	})
}
