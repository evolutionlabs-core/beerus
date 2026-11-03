package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func load(t *testing.T, extra string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	body := "seed: 7\ngenesis: 2024-01-01\n" + extra
	if err := os.WriteFile(filepath.Join(dir, "beerus.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(dir)
}

func TestNIPSessionsAreValidated(t *testing.T) {
	c, err := load(t, "")
	if err != nil || c.Sessions() != DefaultNIPSessions {
		t.Fatalf("default: %v %v", c, err)
	}
	for _, ok := range []string{"1", "2", "12", "24", "1440"} {
		if _, err := load(t, "nip_sessions: "+ok+"\n"); err != nil {
			t.Errorf("nip_sessions %s: %v", ok, err)
		}
	}
	for _, bad := range []string{"-1", "7", "11", "2880", "1000000000"} {
		_, err := load(t, "nip_sessions: "+bad+"\n")
		if err == nil || !strings.Contains(err.Error(), "nip_sessions") {
			t.Errorf("nip_sessions %s: %v", bad, err)
		}
	}
}
