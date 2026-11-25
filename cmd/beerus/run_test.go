package main

import (
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvNumberRejectsBadValues(t *testing.T) {
	for _, c := range []struct {
		value    string
		positive bool
		want     int64
		bad      bool
	}{{"", false, 0, false}, {"-42", false, -42, false}, {"12a", false, 0, true}, {"1.5", true, 0, true}, {"0", true, 0, true}, {"-3", true, 0, true}} {
		t.Setenv("BEERUS_SEED", c.value)
		n, err := envNumber("BEERUS_SEED", c.positive)
		if c.bad != (err != nil) || n != c.want || (c.bad && (!strings.Contains(err.Error(), "BEERUS_SEED") || !strings.Contains(err.Error(), c.value))) {
			t.Errorf("%q: %d, %v", c.value, n, err)
		}
	}
}

func TestInitFromEnvReadsTheSetupFile(t *testing.T) {
	good := "apiVersion: beerus.dev/v1\nkind: Setup\nmetadata:\n  name: beerus\nspec:\n  banks:\n    - id: bank-a\n      name: Bank A\n      type: commercial\n"
	t.Setenv("BEERUS_PARTNER_REDIRECT_URI", "")
	for _, c := range []struct {
		name, content, err string
		banks              int
	}{
		{"no file", "", "", 0},
		{"one bank", good, "", 1},
		{"bad file", "kind: Setup\n", "BEERUS_SETUP_FILE: ", 0},
		{"missing file", "missing", "BEERUS_SETUP_FILE: read ", 0},
	} {
		path := filepath.Join(t.TempDir(), "setup.yaml")
		if c.content == "" || c.content == "missing" {
			path = ""
			if c.content != "" {
				path = filepath.Join(t.TempDir(), "missing.yaml")
			}
		} else if err := os.WriteFile(path, []byte(c.content), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BEERUS_SETUP_FILE", path)
		dir := filepath.Join(t.TempDir(), "state")
		err := initFromEnv(dir)
		if c.err != "" {
			if ok, initErr := setup.Initialized(dir); err == nil || !strings.Contains(err.Error(), c.err) || ok || initErr != nil {
				t.Errorf("%s: error %v, want %q and no state (%v, %v)", c.name, err, c.err, ok, initErr)
			}
			continue
		}
		cfg, loadErr := setup.LoadConfig(dir)
		if err != nil || loadErr != nil || len(cfg.Banks) != c.banks {
			t.Errorf("%s: %v, %v", c.name, err, loadErr)
		}
	}
}
