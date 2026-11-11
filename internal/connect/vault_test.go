package connect

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVaultSealsEachTokenWithItsOwnKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.key")
	v, err := loadVault(path)
	if err != nil {
		t.Fatal(err)
	}
	e, err := v.put("con_1", "oba_secret", "obr_secret", time.Now(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(e)
	if bytes.Contains(raw, []byte("oba_secret")) || bytes.Contains(raw, []byte("obr_secret")) {
		t.Fatal("tokens are stored in the clear")
	}
	if bytes.Equal(e.Access.Wrapped, e.Refresh.Wrapped) {
		t.Fatal("two tokens share a data key")
	}
	got, err := v.open(e.Refresh, "con_1/refresh")
	if err != nil || got != "obr_secret" {
		t.Fatalf("round trip: %q %v", got, err)
	}
	if _, err := v.open(e.Refresh, "con_2/refresh"); err == nil {
		t.Fatal("a token opened under another connection")
	}
	if _, err := v.open(e.Access, "con_1/refresh"); err == nil {
		t.Fatal("an access token opened as a refresh token")
	}
	again, err := loadVault(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := again.open(e.Access, "con_1/access"); err != nil || got != "oba_secret" {
		t.Fatalf("after reload: %q %v", got, err)
	}
	other, _ := loadVault(filepath.Join(t.TempDir(), "other.key"))
	if _, err := other.open(e.Access, "con_1/access"); err == nil {
		t.Fatal("a different master key opened the token")
	}
}

func TestVaultKeyIsCreatedOnlyWhenMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.key")
	if _, err := loadVault(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", info, err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}

	bad := filepath.Join(dir, "bad.key")
	if err := os.WriteFile(bad, []byte("not hex\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadVault(bad); err == nil || err.Error() != "the vault key is malformed" {
		t.Fatalf("malformed key: %v", err)
	}
	if raw, _ := os.ReadFile(bad); string(raw) != "not hex\n" {
		t.Fatalf("a malformed key was overwritten: %q", raw)
	}

	unreadable := filepath.Join(dir, "dir.key")
	if err := os.Mkdir(unreadable, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = loadVault(unreadable)
	if err == nil || !strings.HasPrefix(err.Error(), "read vault key: ") {
		t.Fatalf("unreadable key: %v", err)
	}
	if info, err := os.Stat(unreadable); err != nil || !info.IsDir() {
		t.Fatal("an unreadable key was replaced")
	}
}
