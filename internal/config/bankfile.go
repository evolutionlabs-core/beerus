package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/countrypack"
	"github.com/evolutionlabs-core/beerus/internal/pki"
)

const (
	BankFileName  = "bank.json"
	BankTokenFile = "control-token"
	BankCAFile    = "ca.crt"
	stateActive   = "active"
	maxBankFile   = 1 << 20
)

type BankFile struct {
	ID               string      `json:"id"`
	Name             string      `json:"name"`
	Type             string      `json:"type"`
	NIPCode          string      `json:"nip_code"`
	NUBANCode        string      `json:"nuban_code"`
	HistoryDays      int         `json:"history_days"`
	Port             int         `json:"port"`
	APIPort          int         `json:"api_port"`
	Database         string      `json:"database"`
	SettlesThrough   string      `json:"settles_through,omitempty"`
	State            string      `json:"state"`
	NetDebitCapMinor int64       `json:"net_debit_cap_minor"`
	CollateralMinor  int64       `json:"collateral_minor"`
	API              BankFileAPI `json:"open_banking"`
	Host             string      `json:"host"`
	WidgetPort       int         `json:"widget_port"`
	WidgetTLS        bool        `json:"widget_tls"`
	NIBSSPort        int         `json:"nibss_port"`
	BrandName        string      `json:"brand_name"`
	CoreURL          string      `json:"core_url"`
	SetupKey         uint64      `json:"setup_key,string"`
}

type BankFileAPI struct {
	ConsentMonths    int `json:"consent_months"`
	RefreshDays      int `json:"refresh_days"`
	DormantDays      int `json:"dormant_days"`
	AssertionSeconds int `json:"assertion_seconds"`
}

type BankFileMissingError struct {
	Path string
}

func (e *BankFileMissingError) Error() string {
	return fmt.Sprintf("no bank config at %s; create the bank with beerus bank create first", e.Path)
}

func BankDir(dir, id string) string {
	return filepath.Join(dir, "banks", id)
}

func BankFilePath(dir, id string) string {
	return filepath.Join(BankDir(dir, id), BankFileName)
}

func (c *Config) bankFile(b *Bank) BankFile {
	state := b.State
	if state == "" {
		state = stateActive
	}
	return BankFile{
		ID:               b.ID,
		Name:             b.Name,
		Type:             b.Type,
		NIPCode:          b.NIPCode,
		NUBANCode:        b.NUBANCode,
		HistoryDays:      b.HistoryDays,
		Port:             b.Port,
		APIPort:          b.APIPort,
		Database:         b.Database,
		SettlesThrough:   b.SettlesThrough,
		State:            state,
		NetDebitCapMinor: b.NetDebitCap,
		CollateralMinor:  b.Collateral,
		API:              BankFileAPI(b.API),
		Host:             c.Connect.Host,
		WidgetPort:       c.Connect.WidgetPort,
		WidgetTLS:        c.Connect.WidgetTLS,
		NIBSSPort:        c.Connect.NIBSSPort,
		BrandName:        c.Connect.BrandName,
		CoreURL:          c.ControlURL(),
		SetupKey:         c.SetupKey,
	}
}

func LoadBankFile(dir, id string) (BankFile, error) {
	if id == "" || id != filepath.Base(id) || id == ".." {
		return BankFile{}, fmt.Errorf("%q is not a bank ID", id)
	}
	path := BankFilePath(dir, id)
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return BankFile{}, &BankFileMissingError{Path: path}
	}
	if err != nil {
		return BankFile{}, fmt.Errorf("open bank config: %w", err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxBankFile+1))
	if err != nil {
		return BankFile{}, fmt.Errorf("read %s: %w", path, err)
	}
	if len(raw) > maxBankFile {
		return BankFile{}, fmt.Errorf("%s is larger than %d MiB", path, maxBankFile>>20)
	}
	var bf BankFile
	if err := json.Unmarshal(raw, &bf); err != nil {
		return BankFile{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := bf.check(dir, id); err != nil {
		return BankFile{}, fmt.Errorf("%s: %w", path, err)
	}
	return bf, nil
}

func (f BankFile) check(dir, id string) error {
	switch {
	case f.ID != id:
		return fmt.Errorf("id is %q, want %q", f.ID, id)
	case f.Port <= 0 || f.APIPort <= 0:
		return errors.New("port and api_port must be above zero")
	case f.Database != DatabaseName(dir, id):
		return fmt.Errorf("database is %q, but core names the database of %s %q", f.Database, id, DatabaseName(dir, id))
	case f.SetupKey == 0:
		return errors.New("setup_key is not set; start Beerus with beerus up to write it again")
	case f.State != stateActive && f.State != "suspended" && f.State != "closed":
		return fmt.Errorf("state %q is not active, suspended or closed", f.State)
	}
	return nil
}

func (f BankFile) Bank() *Bank {
	state := f.State
	if state == stateActive {
		state = ""
	}
	return &Bank{
		ID:             f.ID,
		Name:           f.Name,
		Type:           f.Type,
		NIPCode:        f.NIPCode,
		NUBANCode:      f.NUBANCode,
		HistoryDays:    f.HistoryDays,
		Port:           f.Port,
		APIPort:        f.APIPort,
		Database:       f.Database,
		SettlesThrough: f.SettlesThrough,
		State:          state,
		NetDebitCap:    f.NetDebitCapMinor,
		Collateral:     f.CollateralMinor,
		API:            BankAPI(f.API),
	}
}

func (f BankFile) Config(dir, databaseURL string) (*Config, error) {
	pack, err := countrypack.Nigeria()
	if err != nil {
		return nil, fmt.Errorf("load country pack: %w", err)
	}
	c := &Config{
		Dir:         dir,
		DatabaseURL: databaseURL,
		Banks:       []*Bank{f.Bank()},
		SetupKey:    f.SetupKey,
		Connect:     Connect{Host: f.Host, WidgetPort: f.WidgetPort, WidgetTLS: f.WidgetTLS, NIBSSPort: f.NIBSSPort, BrandName: f.BrandName},
	}
	c.buildFees(pack)
	return c, nil
}

func (c *Config) bankFileBytes(b *Bank) ([]byte, error) {
	raw, err := json.MarshalIndent(c.bankFile(b), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode bank config of %s: %w", b.ID, err)
	}
	return append(raw, '\n'), nil
}

func (c *Config) BankFileDigest(b *Bank) (string, error) {
	raw, err := c.bankFileBytes(b)
	if err != nil {
		return "", err
	}
	return HashKey(string(raw)), nil
}

func (c *Config) EnsureBankDir(b *Bank, now time.Time) error {
	root, err := pki.LoadAuthority(c.Path("ca"), "root")
	if err != nil {
		return fmt.Errorf("load root authority: %w", err)
	}
	if err := c.ensureBankCerts(root, b, now); err != nil {
		return err
	}
	dir := BankDir(c.Dir, b.ID)
	token, err := os.ReadFile(c.Path(BankTokenFile))
	if err != nil {
		return fmt.Errorf("read control token: %w", err)
	}
	raw, err := c.bankFileBytes(b)
	if err != nil {
		return err
	}
	files := []struct {
		name string
		body []byte
		mode os.FileMode
	}{
		{BankCAFile, root.PEM, 0o644},
		{BankTokenFile, token, 0o600},
		{BankFileName, raw, 0o600},
	}
	for _, f := range files {
		if err := writeIfChanged(filepath.Join(dir, f.name), f.body, f.mode); err != nil {
			return fmt.Errorf("write %s of bank %s: %w", f.name, b.ID, err)
		}
	}
	return nil
}

func writeIfChanged(path string, body []byte, mode os.FileMode) error {
	have, err := os.ReadFile(path)
	if err == nil && bytes.Equal(have, body) {
		return nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return pki.WriteFile(path, body, mode)
}
