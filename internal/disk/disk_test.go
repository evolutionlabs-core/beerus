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
	if err := Write(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "two" {
		t.Fatalf("content = %q, err %v", b, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("dir entries = %d, err %v", len(entries), err)
	}
	err = Write(filepath.Join(dir, "missing", "secret"), []byte("x"), 0o600)
	if err == nil || !strings.HasPrefix(err.Error(), "create temp file for ") {
		t.Fatalf("error = %v", err)
	}
}
