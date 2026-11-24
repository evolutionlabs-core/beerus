package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/countrypack"
)

const (
	NIBSSID        = "nibss"
	NIBSSFileName  = "nibss.json"
	NIBSSTokenFile = "control-token"
	maxNIBSSFile   = 1 << 20
)

type NIBSSFile struct {
	Database    string `json:"database"`
	Port        int    `json:"port"`
	ControlPort int    `json:"control_port"`
	WorldID     string `json:"world_id"`
	TSQAfter    string `json:"tsq_after"`
	Sessions    int    `json:"sessions"`
	CBNPort     int    `json:"cbn_port"`

	HolidaysAdd    []string `json:"holidays_add"`
	HolidaysRemove []string `json:"holidays_remove"`
}

func (f NIBSSFile) Calendar() (clock.Calendar, error) {
	pack, err := countrypack.Nigeria()
	if err != nil {
		return clock.Calendar{}, fmt.Errorf("load country pack: %w", err)
	}
	add, err := parseHolidays("add", f.HolidaysAdd)
	if err != nil {
		return clock.Calendar{}, err
	}
	remove, err := parseHolidays("remove", f.HolidaysRemove)
	if err != nil {
		return clock.Calendar{}, err
	}
	return clock.NewCalendar(pack.Fixed.Days, add, remove), nil
}

func (f NIBSSFile) StatusQueryAfter() time.Duration {
	d, _ := time.ParseDuration(f.TSQAfter)
	return d
}

type NIBSSFileMissingError struct {
	Path string
}

func (e *NIBSSFileMissingError) Error() string {
	return fmt.Sprintf("no NIBSS config at %s; start Beerus with beerus up first", e.Path)
}

func NIBSSDir(dir string) string {
	return filepath.Join(dir, NIBSSID)
}

func NIBSSFilePath(dir string) string {
	return filepath.Join(NIBSSDir(dir), NIBSSFileName)
}

func LoadNIBSSFile(dir string) (NIBSSFile, error) {
	path := NIBSSFilePath(dir)
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return NIBSSFile{}, &NIBSSFileMissingError{Path: path}
	}
	if err != nil {
		return NIBSSFile{}, fmt.Errorf("open NIBSS config: %w", err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxNIBSSFile+1))
	if err != nil {
		return NIBSSFile{}, fmt.Errorf("read %s: %w", path, err)
	}
	if len(raw) > maxNIBSSFile {
		return NIBSSFile{}, fmt.Errorf("%s is larger than %d MiB", path, maxNIBSSFile>>20)
	}
	var nf NIBSSFile
	if err := json.Unmarshal(raw, &nf); err != nil {
		return NIBSSFile{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := nf.check(dir); err != nil {
		return NIBSSFile{}, fmt.Errorf("%s: %w", path, err)
	}
	return nf, nil
}

func (f NIBSSFile) check(dir string) error {
	switch {
	case f.Port <= 0 || f.ControlPort <= 0:
		return errors.New("port and control_port must be above zero")
	case f.Port == f.ControlPort:
		return fmt.Errorf("port and control_port are both %d; they must differ", f.Port)
	case f.Database != DatabaseName(dir, NIBSSID):
		return fmt.Errorf("database is %q, but core names the NIBSS database %q", f.Database, DatabaseName(dir, NIBSSID))
	case !validDuration(f.TSQAfter):
		return fmt.Errorf("tsq_after needs a duration above zero, not %q", f.TSQAfter)
	case f.Sessions <= 0:
		return fmt.Errorf("sessions must be above zero, not %d", f.Sessions)
	case f.CBNPort <= 0:
		return fmt.Errorf("cbn_port must be above zero, not %d", f.CBNPort)
	}
	return nil
}

func validDuration(s string) bool {
	d, err := time.ParseDuration(s)
	return err == nil && d > 0
}

func (c *Config) nibssFileBytes(worldID string) ([]byte, error) {
	file := NIBSSFile{
		Database:    DatabaseName(c.Dir, NIBSSID),
		Port:        c.Connect.NIBSSPort,
		ControlPort: c.Connect.NIBSSControlPort,
		WorldID:     worldID,
		TSQAfter:    c.TSQAfter.String(),
		Sessions:    c.Sessions(),
		CBNPort:     c.Connect.CBNPort,

		HolidaysAdd:    []string{},
		HolidaysRemove: []string{},
	}
	if c.Holidays != nil {
		file.HolidaysAdd, file.HolidaysRemove = append(file.HolidaysAdd, c.Holidays.Add...), append(file.HolidaysRemove, c.Holidays.Remove...)
	}
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode NIBSS config: %w", err)
	}
	return append(raw, '\n'), nil
}

func (c *Config) EnsureNIBSSDir(worldID string) error {
	dir := NIBSSDir(c.Dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create nibss dir: %w", err)
	}
	raw, err := c.nibssFileBytes(worldID)
	if err != nil {
		return err
	}
	if err := writeIfChanged(filepath.Join(dir, NIBSSFileName), raw, 0o600); err != nil {
		return fmt.Errorf("write %s of NIBSS: %w", NIBSSFileName, err)
	}
	return c.ensureNIBSSToken(dir)
}

func (c *Config) ensureNIBSSToken(dir string) error {
	path := filepath.Join(dir, NIBSSTokenFile)
	_, err := os.Stat(path)
	if err == nil {
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read NIBSS control token: %w", err)
	}
	token, err := randomToken("nbs_", 24)
	if err != nil {
		return fmt.Errorf("make NIBSS control token: %w", err)
	}
	if err := WriteSecret(path, token+"\n"); err != nil {
		return fmt.Errorf("write NIBSS control token: %w", err)
	}
	return nil
}
