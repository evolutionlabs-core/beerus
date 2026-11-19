package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/evolutionlabs-core/beerus/internal/pki"
)

const (
	ConnectStateFile = "state.json"
	legacyStateFile  = "stub-state.json"
)

var connectStateKeys = []string{"world_id", "revokes", "access", "link_sessions", "widget_sessions", "connections", "accounts", "feeds", "refreshes", "deliveries", "idempotency", "seq", "vault"}

func ConnectStatePath(dir string) string {
	return filepath.Join(dir, "connect", ConnectStateFile)
}

func EnsureConnectState(dir string) error {
	path := ConnectStatePath(dir)
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("look for %s: %w", path, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, legacyStateFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", legacyStateFile, err)
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		return fmt.Errorf("parse %s: %w", legacyStateFile, err)
	}
	own := map[string]json.RawMessage{}
	for _, k := range connectStateKeys {
		if v, ok := all[k]; ok {
			own[k] = v
		}
	}
	out, err := json.Marshal(own)
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create directory for %s: %w", path, err)
	}
	return pki.WriteFile(path, out, 0o600)
}
