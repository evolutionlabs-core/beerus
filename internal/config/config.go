package config

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/nip"
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
	CertSHA256   string   `yaml:"cert_sha256"`
}

type Connect struct {
	Host         string `yaml:"host"`
	PartnerPort  int    `yaml:"partner_port"`
	WidgetPort   int    `yaml:"widget_port"`
	WidgetTLS    bool   `yaml:"widget_tls"`
	ControlPort  int    `yaml:"control_port"`
	HealthPort   int    `yaml:"health_port"`
	LegalName    string `yaml:"legal_name"`
	BrandName    string `yaml:"brand_name"`
	RegistryID   string `yaml:"registry_id"`
	CACNumber    string `yaml:"cac_number"`
	SyncInterval string `yaml:"sync_interval"`
	RefreshEvery string `yaml:"refresh_min_interval"`
	PauseLease   string `yaml:"pause_lease,omitempty"`
}

type Bank struct {
	ID             string  `yaml:"id"`
	Name           string  `yaml:"name"`
	Type           string  `yaml:"type"`
	NIPCode        string  `yaml:"nip_code"`
	NUBANCode      string  `yaml:"nuban_code"`
	HistoryDays    int     `yaml:"history_days"`
	Port           int     `yaml:"port"`
	APIPort        int     `yaml:"api_port,omitempty"`
	StableIDs      *bool   `yaml:"stable_ids,omitempty"`
	Database       string  `yaml:"database"`
	SettlesThrough string  `yaml:"settles_through,omitempty"`
	State          string  `yaml:"state,omitempty"`
	NetDebitCap    int64   `yaml:"net_debit_cap,omitempty"`
	Collateral     int64   `yaml:"collateral,omitempty"`
	API            BankAPI `yaml:"open_banking,omitempty"`
}

func (b *Bank) Cap() int64 {
	if b.NetDebitCap > 0 {
		return b.NetDebitCap
	}
	return nip.SimulatorDefaultCap(b.Type)
}

func (b *Bank) CapIsDefault() bool {
	return b.NetDebitCap <= 0
}

func (b *Bank) Suspended() bool {
	return b.State == "suspended"
}

func (b *Bank) Stable() bool {
	return b.StableIDs == nil || *b.StableIDs
}

func (b *Bank) Closed() bool {
	return b.State == "closed"
}

func (b *Bank) URL() string {
	return fmt.Sprintf("http://%s.localhost:%d", b.ID, b.Port)
}

func (b *Bank) APIURL() string {
	return fmt.Sprintf("https://%s.localhost:%d", b.ID, b.APIPort)
}

func (b *Bank) Style() string {
	if b.Type == "digital" {
		return "digital"
	}
	return "traditional"
}

func DatabaseName(dir, bankID string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	return "beerus_" + HashKey(abs)[:12] + "_" + strings.ReplaceAll(bankID, "-", "_")
}

type Holidays struct {
	Add    []string `yaml:"add,omitempty"`
	Remove []string `yaml:"remove,omitempty"`
}

type Config struct {
	DatabaseURL string     `yaml:"database_url"`
	Banks       []*Bank    `yaml:"banks,omitempty"`
	MaxBanks    int        `yaml:"max_banks,omitempty"`
	NIPSessions int        `yaml:"nip_sessions,omitempty"`
	Ports       Ports      `yaml:"ports,omitempty"`
	Seed        int64      `yaml:"seed"`
	Genesis     string     `yaml:"genesis"`
	Pace        float64    `yaml:"pace"`
	Connect     Connect    `yaml:"connect"`
	Partners    []*Partner `yaml:"partners"`
	Holidays    *Holidays  `yaml:"holidays,omitempty"`
	Dir         string     `yaml:"-"`
	calendar    *clock.Calendar

	savedDatabaseURL string
	databaseFromEnv  bool
}

const DatabaseURLEnv = "BEERUS_DATABASE_URL"

func (c *Config) Calendar() *clock.Calendar {
	return c.calendar
}

func (c *Config) buildCalendar() error {
	c.calendar = nil
	if c.Holidays == nil {
		return nil
	}
	parse := func(name string, list []string) ([]clock.Date, error) {
		var out []clock.Date
		for _, s := range list {
			d, err := clock.ParseDate(s)
			if err != nil {
				return nil, fmt.Errorf("beerus.yaml: holidays.%s: %q is not a date (use YYYY-MM-DD)", name, s)
			}
			out = append(out, d)
		}
		return out, nil
	}
	add, err := parse("add", c.Holidays.Add)
	if err != nil {
		return err
	}
	remove, err := parse("remove", c.Holidays.Remove)
	if err != nil {
		return err
	}
	c.calendar = clock.NewCalendar(add, remove)
	return nil
}

const DefaultNIPSessions = 4

func (c *Config) Sessions() int {
	if c.NIPSessions == 0 {
		return DefaultNIPSessions
	}
	return c.NIPSessions
}

func (c *Config) validateSessions() error {
	n := c.Sessions()
	if n < 1 || 24*60%n != 0 {
		return fmt.Errorf("beerus.yaml: nip_sessions must be a whole number of sessions that divides a day into whole minutes, such as 1, 2, 4, 6, 12 or 24, not %d", n)
	}
	return nil
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

func (c *Config) PauseLease() time.Duration {
	return dur(c.Connect.PauseLease, 2*time.Minute)
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

func (c *Config) Bank(id string) *Bank {
	for _, b := range c.Banks {
		if b.ID == id {
			return b
		}
	}
	return nil
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
	if u := os.Getenv(DatabaseURLEnv); u != "" {
		if c.DatabaseURL != u {
			c.savedDatabaseURL = c.DatabaseURL
		}
		c.DatabaseURL, c.databaseFromEnv = u, true
	}
	if c.Seed == 0 {
		return nil, errors.New("beerus.yaml: seed is required")
	}
	if _, err := c.GenesisDate(); err != nil {
		return nil, fmt.Errorf("beerus.yaml: genesis: %w", err)
	}
	if err := c.validateSessions(); err != nil {
		return nil, err
	}
	if err := c.validatePorts(); err != nil {
		return nil, err
	}
	if c.Pace <= 0 {
		c.Pace = 1
	}
	if err := c.buildCalendar(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) Save() error {
	out := *c
	if c.databaseFromEnv {
		out.DatabaseURL = c.savedDatabaseURL
	}
	b, err := yaml.Marshal(&out)
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
	HealthPort  int
	BankPort    int
	BankPorts   []int
	APIPorts    []int
}

func Init(dir string, o InitOptions) (*Config, error) {
	if Exists(dir) {
		return nil, fmt.Errorf("%s already holds a Beerus setup", dir)
	}
	for _, sub := range []string{"ca", "connect", "partners", "webhook", "snapshots"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return nil, err
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
	if o.HealthPort == 0 {
		o.HealthPort = 7490
	}
	if o.BankPort == 0 {
		o.BankPort = 7445
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
			HealthPort:   o.HealthPort,
			LegalName:    "Beerus Connect Limited",
			BrandName:    "Beerus Connect",
			RegistryID:   "OBR-T2-000001",
			CACNumber:    "RC 0000001",
			SyncInterval: "1m",
			RefreshEvery: "1m",
		},
	}
	c.Banks = []*Bank{
		{ID: "bank-a", Name: "Bank A", Type: "commercial", NIPCode: "999001", NUBANCode: "901", HistoryDays: 730, Port: o.BankPort, Database: DatabaseName(dir, "bank-a")},
		{ID: "bank-b", Name: "Bank B", Type: "digital", NIPCode: "999002", NUBANCode: "902", HistoryDays: 180, Port: o.BankPort + 1, Database: DatabaseName(dir, "bank-b")},
	}
	if len(o.BankPorts) == 2 {
		c.Banks[0].Port, c.Banks[1].Port = o.BankPorts[0], o.BankPorts[1]
	}
	for i, b := range c.Banks {
		b.APIPort = b.Port + c.APIPortOffset()
		if len(o.APIPorts) == len(c.Banks) {
			b.APIPort = o.APIPorts[i]
		}
	}
	if err := c.EnsureCertificates(); err != nil {
		return nil, err
	}
	if _, err := WebhookKey(c); err != nil {
		return nil, err
	}
	token, err := randomToken("ctl_", 24)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(c.Path("control-token"), []byte(token+"\n"), 0o600); err != nil {
		return nil, err
	}
	if err := c.Save(); err != nil {
		return nil, err
	}
	return c, nil
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
	if err := c.ensureOpenBanking(root, now); err != nil {
		return err
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
		fp, err := pki.FileThumbprint(filepath.Join(dir, "client.crt"))
		if err != nil {
			return err
		}
		p.CertSHA256 = fp
		if err := os.WriteFile(filepath.Join(dir, "ca.crt"), root.PEM, 0o644); err != nil {
			return err
		}
	}
	return nil
}

const (
	AggregatorCN    = "beerus-connect"
	AggregatorRole  = "aggregator"
	EventSignerName = "events"
	EventSignerRole = "event-signer"
)

func (c *Config) ensureOpenBanking(root *pki.Authority, now time.Time) error {
	dir := c.Path("connect")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if pki.NeedsRenewal(filepath.Join(dir, "client.crt"), now) {
		leaf, err := root.IssueClient(AggregatorCN, AggregatorRole, leafLife)
		if err != nil {
			return err
		}
		if err := leaf.Save(dir, "client"); err != nil {
			return err
		}
	}
	if _, err := c.SigningKey(); err != nil {
		return err
	}
	return c.EnsureBankCertificates(root, now)
}

func (c *Config) EnsureBankCertificates(root *pki.Authority, now time.Time) error {
	if root == nil {
		var err error
		if root, err = pki.LoadAuthority(c.Path("ca"), "root"); err != nil {
			return err
		}
		now = time.Now()
	}
	for _, b := range c.Banks {
		dir := c.Path("banks", b.ID)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if pki.NeedsRenewal(filepath.Join(dir, "server.crt"), now) {
			leaf, err := root.IssueServer([]string{b.ID + ".localhost", "localhost"}, leafLife)
			if err != nil {
				return err
			}
			if err := leaf.Save(dir, "server"); err != nil {
				return err
			}
		}
		if pki.NeedsRenewal(filepath.Join(dir, EventSignerName+".crt"), now) {
			leaf, err := root.IssueSigner(b.ID+"-events", EventSignerRole, leafLife)
			if err != nil {
				return err
			}
			if err := leaf.Save(dir, EventSignerName); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Config) SigningKey() (*ecdsa.PrivateKey, error) {
	path := c.Path("connect", "ob-signing.key")
	if b, err := os.ReadFile(path); err == nil {
		block, _ := pem.Decode(b)
		if block == nil {
			return nil, errors.New("connect/ob-signing.key is malformed")
		}
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		key, ok := k.(*ecdsa.PrivateKey)
		if !ok {
			return nil, errors.New("connect/ob-signing.key is not an EC key")
		}
		return key, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return key, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600)
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

var partnerID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

type PartnerOptions struct {
	ID           string
	Name         string
	LegalName    string
	Purpose      string
	RedirectURIs []string
	WebhookURL   string
}

func (c *Config) AddPartner(o PartnerOptions) (string, error) {
	if !partnerID.MatchString(o.ID) {
		return "", errors.New("partner ID must be lowercase letters, digits and dashes, up to 40 characters")
	}
	if c.Partner(o.ID) != nil {
		return "", fmt.Errorf("partner %s already exists; use partner rotate-key to get new credentials", o.ID)
	}
	if len(o.RedirectURIs) == 0 {
		return "", errors.New("at least one redirect URI is required")
	}
	for _, u := range append(append([]string{}, o.RedirectURIs...), o.WebhookURL) {
		if u == "" {
			continue
		}
		parsed, err := url.Parse(u)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return "", fmt.Errorf("%q is not an http or https URL", u)
		}
	}
	if o.Name == "" {
		o.Name = o.ID
	}
	if o.LegalName == "" {
		o.LegalName = o.Name
	}
	if o.Purpose == "" {
		o.Purpose = "to read your account details, balances and transactions"
	}
	key, err := randomToken("bk_test_", 24)
	if err != nil {
		return "", err
	}
	p := &Partner{ID: o.ID, Name: o.Name, LegalName: o.LegalName, Purpose: o.Purpose, CertCN: o.ID, APIKeySHA256: HashKey(key), RedirectURIs: o.RedirectURIs, WebhookURL: o.WebhookURL}
	if err := os.MkdirAll(c.Path("partners", o.ID), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(c.Path("partners", o.ID, "api-key"), []byte(key+"\n"), 0o600); err != nil {
		return "", err
	}
	c.Partners = append(c.Partners, p)
	if err := c.EnsureCertificates(); err != nil {
		return "", err
	}
	return key, c.Save()
}

func (c *Config) RotatePartner(id string) (string, error) {
	p := c.Partner(id)
	if p == nil {
		return "", fmt.Errorf("no partner %s", id)
	}
	key, err := randomToken("bk_test_", 24)
	if err != nil {
		return "", err
	}
	if err := os.Remove(c.Path("partners", id, "client.crt")); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err := os.WriteFile(c.Path("partners", id, "api-key"), []byte(key+"\n"), 0o600); err != nil {
		return "", err
	}
	p.APIKeySHA256 = HashKey(key)
	if err := c.EnsureCertificates(); err != nil {
		return "", err
	}
	return key, c.Save()
}

func (c *Config) RemovePartner(id string) error {
	if c.Partner(id) == nil {
		return fmt.Errorf("no partner %s", id)
	}
	keep := c.Partners[:0]
	for _, p := range c.Partners {
		if p.ID != id {
			keep = append(keep, p)
		}
	}
	c.Partners = keep
	if err := c.Save(); err != nil {
		return err
	}
	return os.RemoveAll(c.Path("partners", id))
}
