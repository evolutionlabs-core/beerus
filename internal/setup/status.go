package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var ErrOldSetup = errors.New("This setup was made by an older Beerus. Run beerus reset, then beerus init.")

const (
	StatusFile = "control.json"
	statusMode = 0o600
)

func LoadStatus(dir string) (Status, error) {
	raw, err := os.ReadFile(filepath.Join(dir, StatusFile))
	if err != nil {
		return Status{}, fmt.Errorf("read %s: %w", StatusFile, err)
	}
	var head struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return Status{}, fmt.Errorf("decode %s: %w", StatusFile, err)
	}
	switch {
	case head.Version > StatusVersion:
		return Status{}, fmt.Errorf("%s has version %d, but this beerus reads up to version %d. Upgrade beerus", StatusFile, head.Version, StatusVersion)
	case head.Version < StatusVersion:
		return Status{}, fmt.Errorf("%s: %w", dir, ErrOldSetup)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var s Status
	if err := dec.Decode(&s); err != nil {
		return Status{}, fmt.Errorf("decode %s: %w", StatusFile, err)
	}
	return s, nil
}

func IsCorrupt(err error) bool {
	var syntax *json.SyntaxError
	var kind *json.UnmarshalTypeError
	return errors.As(err, &syntax) || errors.As(err, &kind)
}

func SaveStatus(dir string, s Status) error {
	s.Version = StatusVersion
	raw, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("encode %s: %w", StatusFile, err)
	}
	tmp, err := os.CreateTemp(dir, StatusFile+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp %s: %w", StatusFile, err)
	}
	done := false
	defer func() {
		if !done {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if err := tmp.Chmod(statusMode); err != nil {
		return fmt.Errorf("chmod temp %s: %w", StatusFile, err)
	}
	if _, err := tmp.Write(raw); err != nil {
		return fmt.Errorf("write temp %s: %w", StatusFile, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp %s: %w", StatusFile, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp %s: %w", StatusFile, err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, StatusFile)); err != nil {
		return fmt.Errorf("rename %s: %w", StatusFile, err)
	}
	done = true
	return nil
}

func FindResource(st *Status, kind, id string) *Resource {
	for i := range st.Resources {
		if st.Resources[i].Kind == kind && st.Resources[i].ID == id {
			return &st.Resources[i]
		}
	}
	return nil
}

func (st Status) Clone() (Status, error) {
	raw, err := json.Marshal(st)
	if err != nil {
		return Status{}, fmt.Errorf("copy status: %w", err)
	}
	var out Status
	if err := json.Unmarshal(raw, &out); err != nil {
		return Status{}, fmt.Errorf("copy status: %w", err)
	}
	return out, nil
}
