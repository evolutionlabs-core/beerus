package connect

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"time"
)

const (
	vaultKeyBytes   = 32
	linkVaultPrefix = "ls:"
)

type Sealed struct {
	Wrapped  []byte `json:"wrapped"`
	KeyNonce []byte `json:"key_nonce"`
	Nonce    []byte `json:"nonce"`
	Data     []byte `json:"data"`
}

type VaultEntry struct {
	Access         Sealed    `json:"access"`
	Refresh        Sealed    `json:"refresh"`
	AccessExpires  time.Time `json:"access_expires"`
	RefreshExpires time.Time `json:"refresh_expires"`
}

type vault struct {
	master []byte
}

var errVault = errors.New("the stored token cannot be read")

func loadVault(path string) (*vault, error) {
	if b, err := os.ReadFile(path); err == nil {
		key, err := hex.DecodeString(strings.TrimSpace(string(b)))
		if err != nil || len(key) != vaultKeyBytes {
			return nil, errors.New("the vault key is malformed")
		}
		return &vault{master: key}, nil
	}
	key := make([]byte, vaultKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(key)+"\n"), 0o600); err != nil {
		return nil, err
	}
	return &vault{master: key}, nil
}

func gcm(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (v *vault) seal(plain, aad string) (Sealed, error) {
	dk := make([]byte, vaultKeyBytes)
	if _, err := rand.Read(dk); err != nil {
		return Sealed{}, err
	}
	inner, err := gcm(dk)
	if err != nil {
		return Sealed{}, err
	}
	outer, err := gcm(v.master)
	if err != nil {
		return Sealed{}, err
	}
	s := Sealed{Nonce: make([]byte, inner.NonceSize()), KeyNonce: make([]byte, outer.NonceSize())}
	if _, err := rand.Read(s.Nonce); err != nil {
		return Sealed{}, err
	}
	if _, err := rand.Read(s.KeyNonce); err != nil {
		return Sealed{}, err
	}
	s.Data = inner.Seal(nil, s.Nonce, []byte(plain), []byte(aad))
	s.Wrapped = outer.Seal(nil, s.KeyNonce, dk, []byte(aad))
	return s, nil
}

func (v *vault) open(s Sealed, aad string) (string, error) {
	outer, err := gcm(v.master)
	if err != nil {
		return "", err
	}
	if len(s.KeyNonce) != outer.NonceSize() {
		return "", errVault
	}
	dk, err := outer.Open(nil, s.KeyNonce, s.Wrapped, []byte(aad))
	if err != nil {
		return "", errVault
	}
	inner, err := gcm(dk)
	if err != nil || len(s.Nonce) != inner.NonceSize() {
		return "", errVault
	}
	plain, err := inner.Open(nil, s.Nonce, s.Data, []byte(aad))
	if err != nil {
		return "", errVault
	}
	return string(plain), nil
}

func (v *vault) put(id, access, refresh string, accessExp, refreshExp time.Time) (*VaultEntry, error) {
	a, err := v.seal(access, id+"/access")
	if err != nil {
		return nil, err
	}
	r, err := v.seal(refresh, id+"/refresh")
	if err != nil {
		return nil, err
	}
	return &VaultEntry{Access: a, Refresh: r, AccessExpires: accessExp, RefreshExpires: refreshExp}, nil
}

func (a *App) vaultPut(id string, t bankToken) error {
	e, err := a.vault.put(id, t.Access, t.Refresh, t.AccessExpires, t.RefreshExpires)
	if err != nil {
		return err
	}
	a.st.Vault[id] = e
	a.dirty()
	return nil
}

func (a *App) vaultMove(from, to string) error {
	e := a.st.Vault[from]
	if e == nil {
		return errVault
	}
	access, err := a.vault.open(e.Access, from+"/access")
	if err != nil {
		return err
	}
	refresh, err := a.vault.open(e.Refresh, from+"/refresh")
	if err != nil {
		return err
	}
	delete(a.st.Vault, from)
	return a.vaultPut(to, bankToken{Access: access, Refresh: refresh, AccessExpires: e.AccessExpires, RefreshExpires: e.RefreshExpires})
}

func (a *App) vaultDrop(id string) {
	if _, ok := a.st.Vault[id]; ok {
		delete(a.st.Vault, id)
		a.dirty()
	}
}
