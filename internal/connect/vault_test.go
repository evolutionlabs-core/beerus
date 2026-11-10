package connect

import (
	"bytes"
	"encoding/json"
	"path/filepath"
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
