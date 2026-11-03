package config

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

type Resource struct {
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	Bank      string    `json:"bank,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type Manifest struct {
	Resources []Resource `json:"resources"`
}

var manifestMu sync.Mutex

func (c *Config) manifestPath() string {
	return c.Path("manifest.json")
}

func (c *Config) Manifest() (Manifest, error) {
	manifestMu.Lock()
	defer manifestMu.Unlock()
	return c.loadManifest()
}

func (c *Config) loadManifest() (Manifest, error) {
	var m Manifest
	b, err := os.ReadFile(c.manifestPath())
	if os.IsNotExist(err) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(b, &m)
}

func (c *Config) saveManifest(m Manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.manifestPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.manifestPath())
}

func (c *Config) Claim(kind, name, bank string) error {
	manifestMu.Lock()
	defer manifestMu.Unlock()
	m, err := c.loadManifest()
	if err != nil {
		return err
	}
	for _, r := range m.Resources {
		if r.Kind == kind && r.Name == name {
			return nil
		}
	}
	m.Resources = append(m.Resources, Resource{Kind: kind, Name: name, Bank: bank, CreatedAt: time.Now().UTC()})
	return c.saveManifest(m)
}

func (c *Config) Release(kind, name string) error {
	manifestMu.Lock()
	defer manifestMu.Unlock()
	m, err := c.loadManifest()
	if err != nil {
		return err
	}
	keep := m.Resources[:0]
	for _, r := range m.Resources {
		if !(r.Kind == kind && r.Name == name) {
			keep = append(keep, r)
		}
	}
	m.Resources = keep
	return c.saveManifest(m)
}

func (c *Config) ReleaseBank(bank string) error {
	manifestMu.Lock()
	defer manifestMu.Unlock()
	m, err := c.loadManifest()
	if err != nil {
		return err
	}
	keep := m.Resources[:0]
	for _, r := range m.Resources {
		if r.Bank != bank {
			keep = append(keep, r)
		}
	}
	m.Resources = keep
	return c.saveManifest(m)
}
