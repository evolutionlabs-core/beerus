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
	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/state"
)

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
	err := Install(ctx, dir, banktest.Spec("beerus"), local, today, func(s InstallStep) {
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

	freshLines := []string{
		"Checking Postgres||ok",
		"Writing config||ok",
		"Reserving CBN and NIBSS database names||ok",
		"Preparing CBN ports and certificates||ok",
	}
	resumed := slices.Clone(freshLines)
	resumed[1] = "Writing config|already written|ok"

	cases := []struct {
		name        string
		cancelAfter string
		wantFirst   []string
		wantFirstEr string
		wantResume  []string
	}{
		{
			name:      "fresh install, then a rerun is refused",
			wantFirst: freshLines,
		},
		{
			name:        "resume after Reserving CBN and NIBSS database names fails",
			cancelAfter: "Writing config",
			wantFirst:   []string{"Checking Postgres||ok", "Writing config||ok", "Reserving CBN and NIBSS database names||failed"},
			wantFirstEr: "context canceled",
			wantResume:  resumed,
		},
		{
			name:        "resume after Preparing CBN ports and certificates fails",
			cancelAfter: "Reserving CBN and NIBSS database names",
			wantFirst: []string{
				"Checking Postgres||ok",
				"Writing config||ok",
				"Reserving CBN and NIBSS database names||ok",
				"Preparing CBN ports and certificates||failed",
			},
			wantFirstEr: "context canceled",
			wantResume:  resumed,
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
			if !slices.Equal(got, c.wantFirst) {
				t.Fatalf("first run:\n got %q\nwant %q", got, c.wantFirst)
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
				if !slices.Equal(got, c.wantResume) {
					t.Fatalf("resume:\n got %q\nwant %q", got, c.wantResume)
				}
			}
			st, err := setup.LoadStatus(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{setup.SystemCBN, setup.SystemNIBSS} {
				if r := state.FindResource(&st, setup.KindSystem, id); r == nil || r.Phase != config.PhaseActive {
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

	t.Run("the CBN and NIBSS make their own databases", func(t *testing.T) {
		dir := t.TempDir()
		t.Cleanup(func() {
			if _, err := Reset(context.Background(), dir, url); err != nil {
				t.Errorf("drop databases: %v", err)
			}
		})
		if _, err := runInstall(t.Context(), dir, url, nil); err != nil {
			t.Fatal(err)
		}
		st, err := setup.LoadStatus(dir)
		if err != nil {
			t.Fatal(err)
		}
		made, err := store.List(t.Context(), url, config.DatabaseName(dir, ""))
		if err != nil {
			t.Fatal(err)
		}
		claims := state.Owned(st, state.OwnedDatabase)
		for _, id := range []string{setup.SystemCBN, setup.SystemNIBSS} {
			name := config.DatabaseName(dir, id)
			if claims[name] != setup.KindSystem+"/"+id {
				t.Errorf("%s is not claimed: %v", name, claims)
			}
			if slices.Contains(made, name) {
				t.Errorf("install made %s", name)
			}
		}
		r := state.FindResource(&st, setup.KindSystem, setup.SystemCBN)
		if r == nil || r.Allocated.Port == 0 || r.Allocated.APIPort == 0 {
			t.Errorf("CBN ports: %+v", r)
		}
		if _, err := os.Stat(filepath.Join(config.CBNDir(dir), config.CBNTokenFile)); err != nil {
			t.Errorf("CBN directory: %v", err)
		}
	})
}
