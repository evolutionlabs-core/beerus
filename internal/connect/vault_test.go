package connect

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/world"
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

func TestFingerprintIgnoresBankIDs(t *testing.T) {
	d, _ := clock.ParseDate("2026-10-01")
	mk := func(prefix string) []world.Presented {
		return []world.Presented{
			{Key: prefix + "1", Amount: -5000, ValueDate: d, Narration: "STAMP DUTY", BalanceAfter: 95000, Seq: 1},
			{Key: prefix + "2", Amount: -5000, ValueDate: d, Narration: "STAMP DUTY", BalanceAfter: 90000, Seq: 2},
			{Key: prefix + "3", Pending: true, Amount: -700, ValueDate: d, Narration: "POS", Seq: 3},
			{Key: prefix + "4", Pending: true, Amount: -700, ValueDate: d, Narration: "POS", Seq: 4},
		}
	}
	a, b := mk("x"), mk("y")
	fingerprint(a)
	fingerprint(b)
	ids := map[string]bool{}
	for i := range a {
		if a[i].Key != b[i].Key {
			t.Fatalf("line %d changed identity", i)
		}
		ids[a[i].Key] = true
	}
	if len(ids) != 4 {
		t.Fatalf("identical lines collided: %d distinct", len(ids))
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
