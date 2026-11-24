package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	CBNID        = "cbn"
	CBNFileName  = "cbn.json"
	CBNTokenFile = "control-token"
	CBNHost      = "cbn.localhost"
	maxCBNFile   = 1 << 20
)

type CBNFile struct {
	Database    string `json:"database"`
	Port        int    `json:"port"`
	ControlPort int    `json:"control_port"`
	WorldID     string `json:"world_id"`
}

type CBNFileMissingError struct {
	Path string
}

func (e *CBNFileMissingError) Error() string {
	return fmt.Sprintf("no CBN config at %s; start Beerus with beerus up first", e.Path)
}

func CBNDir(dir string) string {
	return filepath.Join(dir, CBNID)
}

func CBNFilePath(dir string) string {
	return filepath.Join(CBNDir(dir), CBNFileName)
}

func LoadCBNFile(dir string) (CBNFile, error) {
	path := CBNFilePath(dir)
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return CBNFile{}, &CBNFileMissingError{Path: path}
	}
	if err != nil {
		return CBNFile{}, fmt.Errorf("open CBN config: %w", err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxCBNFile+1))
	if err != nil {
		return CBNFile{}, fmt.Errorf("read %s: %w", path, err)
	}
	if len(raw) > maxCBNFile {
		return CBNFile{}, fmt.Errorf("%s is larger than %d MiB", path, maxCBNFile>>20)
	}
	var cf CBNFile
	if err := json.Unmarshal(raw, &cf); err != nil {
		return CBNFile{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cf.check(dir); err != nil {
		return CBNFile{}, fmt.Errorf("%s: %w", path, err)
	}
	return cf, nil
}

func (f CBNFile) check(dir string) error {
	switch {
	case f.Port <= 0 || f.ControlPort <= 0:
		return errors.New("port and control_port must be above zero")
	case f.Port == f.ControlPort:
		return fmt.Errorf("port and control_port are both %d; they must differ", f.Port)
	case f.Database != DatabaseName(dir, CBNID):
		return fmt.Errorf("database is %q, but Beerus names the CBN database %q", f.Database, DatabaseName(dir, CBNID))
	}
	return nil
}

func (c *Config) cbnFileBytes() ([]byte, error) {
	raw, err := json.MarshalIndent(CBNFile{
		Database:    DatabaseName(c.Dir, CBNID),
		Port:        c.Connect.CBNPort,
		ControlPort: c.Connect.CBNControlPort,
		WorldID:     fmt.Sprintf("wld_%d", c.SetupKey),
	}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode CBN config: %w", err)
	}
	return append(raw, '\n'), nil
}

func (c *Config) EnsureCBNDir() error {
	dir := CBNDir(c.Dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create cbn dir: %w", err)
	}
	raw, err := c.cbnFileBytes()
	if err != nil {
		return err
	}
	if err := writeIfChanged(filepath.Join(dir, CBNFileName), raw, 0o600); err != nil {
		return fmt.Errorf("write %s of CBN: %w", CBNFileName, err)
	}
	return c.ensureCBNToken(dir)
}

func (c *Config) ensureCBNToken(dir string) error {
	path := filepath.Join(dir, CBNTokenFile)
	_, err := os.Stat(path)
	if err == nil {
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read CBN control token: %w", err)
	}
	token, err := randomToken("cbn_", 24)
	if err != nil {
		return fmt.Errorf("make CBN control token: %w", err)
	}
	if err := WriteSecret(path, token+"\n"); err != nil {
		return fmt.Errorf("write CBN control token: %w", err)
	}
	return nil
}
