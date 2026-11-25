package disk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteReplacesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	for _, s := range []string{"one", "two"} {
		if err := Write(path, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "two" {
		t.Fatalf("content %q, %v", b, err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 {
		t.Fatalf("%d entries, %v", len(entries), err)
	}
	if err := Write(filepath.Join(dir, "missing", "secret"), nil, 0o600); err == nil || !strings.HasPrefix(err.Error(), "create temp file for ") {
		t.Fatalf("error = %v", err)
	}
}
