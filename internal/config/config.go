package config

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/pki"
)

type Partner struct {
	ID           string   `yaml:"id"`
	Name         string   `yaml:"name"`
	LegalName    string   `yaml:"legal_name"`
	Purpose      string   `yaml:"purpose"`
	CertCN       string   `yaml:"cert_cn"`
	APIKeySHA256 string   `yaml:"api_key_sha256"`
	RedirectURIs []string `yaml:"redirect_uris"`
	WebhookURL   string   `yaml:"webhook_url"`
}

type Connect struct {
	Host         string `yaml:"host"`
	PartnerPort  int    `yaml:"partner_port"`
	WidgetPort   int    `yaml:"widget_port"`
	WidgetTLS    bool   `yaml:"widget_tls"`
	ControlPort  int    `yaml:"control_port"`
	LegalName    string `yaml:"legal_name"`
	BrandName    string `yaml:"brand_name"`
	RegistryID   string `yaml:"registry_id"`
	CACNumber    string `yaml:"cac_number"`
	SyncInterval string `yaml:"sync_interval"`
	RefreshEvery string `yaml:"refresh_min_interval"`
}

type Config struct {
	Seed     int64      `yaml:"seed"`
	Genesis  string     `yaml:"genesis"`
	Pace     float64    `yaml:"pace"`
	Connect  Connect    `yaml:"connect"`
	Partners []*Partner `yaml:"partners"`
	Dir      string     `yaml:"-"`
}

func (c *Config) Path(parts ...string) string {
	return filepath.Join(append([]string{c.Dir}, parts...)...)
}

func (c *Config) GenesisDate() (clock.Date, error) {
	return clock.ParseDate(c.Genesis)
}

func (c *Config) SyncInterval() time.Duration {
	return dur(c.Connect.SyncInterval, time.Minute)
}

func (c *Config) RefreshMinInterval() time.Duration {
	return dur(c.Connect.RefreshEvery, time.Minute)
}

func dur(s string, def time.Duration) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

func (c *Config) PartnerByCN(cn string) *Partner {
	for _, p := range c.Partners {
		if p.CertCN == cn {
			return p
		}
	}
	return nil
}

func (c *Config) Partner(id string) *Partner {
	for _, p := range c.Partners {
		if p.ID == id {
			return p
		}
	}
	return nil
}

func (c *Config) PartnerURL() string {
	return fmt.Sprintf("https://%s:%d", c.Connect.Host, c.Connect.PartnerPort)
}

func (c *Config) WidgetURL() string {
	scheme := "http"
	if c.Connect.WidgetTLS {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s:%d", scheme, c.Connect.Host, c.Connect.WidgetPort)
}

func (c *Config) ControlURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", c.Connect.ControlPort)
}

func HashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func Load(dir string) (*Config, error) {
	b, err := os.ReadFile(filepath.Join(dir, "beerus.yaml"))
	if err != nil {
		return nil, err
	}
	c := &Config{}
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("beerus.yaml: %w", err)
	}
	c.Dir = dir
	if c.Seed == 0 {
		return nil, errors.New("beerus.yaml: seed is required")
	}
	if _, err := c.GenesisDate(); err != nil {
		return nil, fmt.Errorf("beerus.yaml: genesis: %w", err)
	}
	if c.Pace <= 0 {
		c.Pace = 1
	}
	return c, nil
}

func (c *Config) Save() error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(c.Path("beerus.yaml"), b, 0o600)
}

func Exists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "beerus.yaml"))
	return err == nil
}

func randomToken(prefix string, n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + strings.TrimRight(base64.RawURLEncoding.EncodeToString(b), "="), nil
}

type InitOptions struct {
	Seed        int64
	Today       clock.Date
	PartnerPort int
	WidgetPort  int
	ControlPort int
	WebhookURL  string
	RedirectURI string
}

func Init(dir string, o InitOptions) (*Config, string, error) {
	if Exists(dir) {
		return nil, "", fmt.Errorf("%s already holds a Beerus setup", dir)
	}
	for _, sub := range []string{"ca", "connect", "partners/balance", "webhook", "snapshots"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return nil, "", err
		}
	}
	if o.Seed == 0 {
		o.Seed = 20261028
	}
	if o.PartnerPort == 0 {
		o.PartnerPort = 7443
	}
	if o.WidgetPort == 0 {
		o.WidgetPort = 7444
	}
	if o.ControlPort == 0 {
		o.ControlPort = 7480
	}
	if o.WebhookURL == "" {
		o.WebhookURL = "http://localhost:3000/webhooks/beerus"
	}
	if o.RedirectURI == "" {
		o.RedirectURI = "http://localhost:3000/bank_connections/callback"
	}
	apiKey, err := randomToken("bk_test_", 24)
	if err != nil {
		return nil, "", err
	}
	c := &Config{
		Dir:     dir,
		Seed:    o.Seed,
		Genesis: o.Today.AddDays(-760).String(),
		Pace:    1,
		Connect: Connect{
			Host:         "connect.localhost",
			PartnerPort:  o.PartnerPort,
			WidgetPort:   o.WidgetPort,
			ControlPort:  o.ControlPort,
			LegalName:    "Beerus Connect Limited",
			BrandName:    "Beerus Connect",
			RegistryID:   "OBR-T2-000001",
			CACNumber:    "RC 0000001",
			SyncInterval: "1m",
			RefreshEvery: "1m",
		},
		Partners: []*Partner{{
			ID:           "balance",
			Name:         "Balance",
			LegalName:    "Evolution Labs",
			Purpose:      "to keep your books in sync with this account",
			CertCN:       "balance",
			APIKeySHA256: HashKey(apiKey),
			RedirectURIs: []string{o.RedirectURI},
			WebhookURL:   o.WebhookURL,
		}},
	}
	if err := os.WriteFile(c.Path("partners", "balance", "api-key"), []byte(apiKey+"\n"), 0o600); err != nil {
		return nil, "", err
	}
	if err := c.EnsureCertificates(); err != nil {
		return nil, "", err
	}
	if _, err := WebhookKey(c); err != nil {
		return nil, "", err
	}
	token, err := randomToken("ctl_", 24)
	if err != nil {
		return nil, "", err
	}
	if err := os.WriteFile(c.Path("control-token"), []byte(token+"\n"), 0o600); err != nil {
		return nil, "", err
	}
	if err := c.Save(); err != nil {
		return nil, "", err
	}
	return c, apiKey, nil
}

const (
	leafLife = 90 * 24 * time.Hour
	caLife   = 10 * 365 * 24 * time.Hour
)

func (c *Config) EnsureCertificates() error {
	root, err := loadOrCreateCA(c.Path("ca"), "root", "Beerus CBN Trust Anchor")
	if err != nil {
		return err
	}
	partnerCA, err := loadOrCreateCA(c.Path("ca"), "connect-partners", "Beerus Connect Partner CA")
	if err != nil {
		return err
	}
	now := time.Now()
	server := c.Path("connect", "server.crt")
	if pki.NeedsRenewal(server, now) {
		leaf, err := root.IssueServer([]string{c.Connect.Host, "localhost"}, leafLife)
		if err != nil {
			return err
		}
		if err := leaf.Save(c.Path("connect"), "server"); err != nil {
			return err
		}
	}
	for _, p := range c.Partners {
		dir := c.Path("partners", p.ID)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if pki.NeedsRenewal(filepath.Join(dir, "client.crt"), now) {
			leaf, err := partnerCA.IssueClient(p.CertCN, "partner", leafLife)
			if err != nil {
				return err
			}
			if err := leaf.Save(dir, "client"); err != nil {
				return err
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "ca.crt"), root.PEM, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func loadOrCreateCA(dir, name, cn string) (*pki.Authority, error) {
	if a, err := pki.LoadAuthority(dir, name); err == nil {
		return a, nil
	}
	a, err := pki.NewAuthority(cn, caLife)
	if err != nil {
		return nil, err
	}
	return a, a.Save(dir, name)
}

type SigningKey struct {
	ID      string
	Private ed25519.PrivateKey
}

func (k SigningKey) PublicWH() string {
	return "whpk_" + base64.StdEncoding.EncodeToString(k.Private.Public().(ed25519.PublicKey))
}

func (k SigningKey) SecretWH() string {
	return "whsk_" + base64.StdEncoding.EncodeToString(k.Private.Seed())
}

func WebhookKey(c *Config) (SigningKey, error) {
	path := c.Path("webhook", "signing.key")
	b, err := os.ReadFile(path)
	if err == nil {
		s := strings.TrimSpace(string(b))
		id, secret, ok := strings.Cut(s, " ")
		if !ok || !strings.HasPrefix(secret, "whsk_") {
			return SigningKey{}, errors.New("webhook signing key is malformed")
		}
		seed, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsk_"))
		if err != nil || len(seed) != ed25519.SeedSize {
			return SigningKey{}, errors.New("webhook signing key is malformed")
		}
		return SigningKey{ID: id, Private: ed25519.NewKeyFromSeed(seed)}, nil
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return SigningKey{}, err
	}
	id, err := randomToken("whk_", 8)
	if err != nil {
		return SigningKey{}, err
	}
	k := SigningKey{ID: id, Private: priv}
	if err := os.WriteFile(path, []byte(id+" "+k.SecretWH()+"\n"), 0o600); err != nil {
		return SigningKey{}, err
	}
	if err := os.WriteFile(c.Path("webhook", "public.key"), []byte(k.PublicWH()+"\n"), 0o644); err != nil {
		return SigningKey{}, err
	}
	return k, nil
}

func ReadSecret(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
