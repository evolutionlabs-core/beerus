package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/connect"
)

func TestEnvNumberRejectsBadValues(t *testing.T) {
	t.Setenv("BEERUS_SEED", "")
	if n, err := envNumber("BEERUS_SEED", false); n != 0 || err != nil {
		t.Errorf("unset: %d %v", n, err)
	}
	t.Setenv("BEERUS_SEED", "-42")
	if n, err := envNumber("BEERUS_SEED", false); n != -42 || err != nil {
		t.Errorf("seed: %d %v", n, err)
	}
	for _, c := range []struct {
		value    string
		positive bool
	}{{"12a", false}, {"1.5", true}, {"0", true}, {"-3", true}} {
		t.Setenv("BEERUS_SEED", c.value)
		if _, err := envNumber("BEERUS_SEED", c.positive); err == nil || !strings.Contains(err.Error(), "BEERUS_SEED") || !strings.Contains(err.Error(), c.value) {
			t.Errorf("%q accepted or unnamed: %v", c.value, err)
		}
	}
}

func TestInitFromEnvReadsTheSetupFile(t *testing.T) {
	good := "apiVersion: beerus.dev/v1\nkind: Setup\nmetadata:\n  name: beerus\nspec:\n  start: \"2026-11-04T08:00:00+01:00\"\n  banks:\n    - id: bank-a\n      name: Bank A\n      type: commercial\n    - id: bank-b\n      name: Bank B\n      type: digital\n"
	for _, c := range []struct {
		name    string
		content string
		missing bool
		banks   int
		err     string
	}{
		{name: "no file"},
		{name: "two banks", content: good, banks: 2},
		{name: "bad file", content: "kind: Setup\n", err: "BEERUS_SETUP_FILE: "},
		{name: "missing file", missing: true, err: "BEERUS_SETUP_FILE: read "},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("BEERUS_PARTNER_REDIRECT_URI", "")
			path := ""
			if c.missing {
				path = filepath.Join(t.TempDir(), "missing.yaml")
			}
			if c.content != "" {
				path = filepath.Join(t.TempDir(), "setup.yaml")
				if err := os.WriteFile(path, []byte(c.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("BEERUS_SETUP_FILE", path)
			dir := filepath.Join(t.TempDir(), "state")
			err := initFromEnv(dir)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("error %v, want %q", err, c.err)
				}
				if ok, _ := connect.Initialized(dir); ok {
					t.Fatal("a bad setup file still created the state")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := connect.LoadConfig(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(cfg.Banks) != c.banks {
				t.Fatalf("%d banks, want %d", len(cfg.Banks), c.banks)
			}
		})
	}
}
