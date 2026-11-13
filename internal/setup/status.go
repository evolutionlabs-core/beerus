package setup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	statusFile = "control.json"
	statusMode = 0o600
)

func LoadStatus(dir string) (Status, error) {
	raw, err := os.ReadFile(filepath.Join(dir, statusFile))
	if err != nil {
		return Status{}, fmt.Errorf("read %s: %w", statusFile, err)
	}
	var head struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return Status{}, fmt.Errorf("decode %s: %w", statusFile, err)
	}
	switch {
	case head.Version > StatusVersion:
		return Status{}, fmt.Errorf("%s has version %d, but this beerus reads up to version %d. Upgrade beerus", statusFile, head.Version, StatusVersion)
	case head.Version < 1:
		return Status{}, fmt.Errorf("%s has no valid version (got %d). Run beerus reset to start again", statusFile, head.Version)
	}
	raw, err = dropRetiredStart(raw)
	if err != nil {
		return Status{}, fmt.Errorf("decode %s: %w", statusFile, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var s Status
	if err := dec.Decode(&s); err != nil {
		return Status{}, fmt.Errorf("decode %s: %w", statusFile, err)
	}
	return s, nil
}

func dropRetiredStart(raw []byte) ([]byte, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, err
	}
	if len(top["applied"]) == 0 {
		return raw, nil
	}
	var applied map[string]json.RawMessage
	if err := json.Unmarshal(top["applied"], &applied); err != nil {
		return nil, fmt.Errorf("applied: %w", err)
	}
	if len(applied["spec"]) == 0 {
		return raw, nil
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(applied["spec"], &body); err != nil {
		return nil, fmt.Errorf("applied spec: %w", err)
	}
	if _, ok := body["start"]; !ok {
		return raw, nil
	}
	delete(body, "start")
	var err error
	if applied["spec"], err = json.Marshal(body); err != nil {
		return nil, err
	}
	if top["applied"], err = json.Marshal(applied); err != nil {
		return nil, err
	}
	return json.Marshal(top)
}

func SaveStatus(dir string, s Status) error {
	s.Version = StatusVersion
	raw, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("encode %s: %w", statusFile, err)
	}
	tmp, err := os.CreateTemp(dir, statusFile+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp %s: %w", statusFile, err)
	}
	done := false
	defer func() {
		if !done {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if err := tmp.Chmod(statusMode); err != nil {
		return fmt.Errorf("chmod temp %s: %w", statusFile, err)
	}
	if _, err := tmp.Write(raw); err != nil {
		return fmt.Errorf("write temp %s: %w", statusFile, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp %s: %w", statusFile, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp %s: %w", statusFile, err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, statusFile)); err != nil {
		return fmt.Errorf("rename %s: %w", statusFile, err)
	}
	done = true
	return nil
}
