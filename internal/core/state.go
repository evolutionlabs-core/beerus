package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/evolutionlabs-core/beerus/internal/control"
	"github.com/evolutionlabs-core/beerus/internal/disk"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

type State struct {
	SettleNow []string         `json:"settle_now,omitempty"`
	WorldID   string           `json:"world_id"`
	Faults    []*control.Fault `json:"faults"`
	dropped   droppedState
}

func newState() *State {
	return &State{WorldID: web.NewID("wld")}
}

func loadState(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s := &State{}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, err
	}
	var old struct {
		Registry      map[string]json.RawMessage `json:"registry"`
		ConsentLedger []json.RawMessage          `json:"consent_ledger"`
	}
	if err := json.Unmarshal(b, &old); err != nil {
		return nil, err
	}
	s.dropped = droppedState{registry: len(old.Registry), consents: len(old.ConsentLedger)}
	return s, nil
}

type droppedState struct {
	registry int
	consents int
}

func saveJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create directory for %s: %w", path, err)
	}
	return disk.Write(path, b, 0o600)
}
