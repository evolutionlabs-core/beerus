package setup

import (
	"encoding/json"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/nibss"
)

const (
	APIVersion = "beerus.dev/v1"
	KindSetup  = "Setup"

	KindBank    = "Bank"
	KindPartner = "Partner"
	KindSystem  = "System"

	SystemCBN   = "cbn"
	SystemNIBSS = "nibss"

	StateActive    = "active"
	StateSuspended = "suspended"
	StateClosed    = "closed"

	MoneyTransfer = "transfer"
	MoneyPayout   = "payout"
	MoneyLeave    = "leave"

	PhaseProvisioning = "provisioning"
	PhaseActive       = "active"
	PhaseSuspended    = "suspended"
	PhaseRepairing    = "repairing"
	PhaseClosing      = "closing"
	PhaseClosed       = "closed"
	PhaseTerminating  = "terminating"

	OpCreate = "create"
	OpUpdate = "update"
	OpClose  = "close"
	OpRemove = "remove"
	OpDrift  = "drift"

	ConditionReady       = "Ready"
	ConditionProgressing = "Progressing"
	ConditionDegraded    = "Degraded"
	ConditionBlocked     = "Blocked"

	ReasonTerminating         = "Terminating"
	ReasonSettlementPending   = "SettlementPending"
	ReasonMoneyDecisionNeeded = "MoneyDecisionNeeded"
	ReasonTargetUnavailable   = "TargetUnavailable"
	ReasonClosed              = "Closed"
	ReasonProvisioning        = "Provisioning"
	ReasonStepFailed          = "StepFailed"

	FinalizerStopRouting       = "stop-routing"
	FinalizerRevokeConnections = "revoke-connections"
	FinalizerSettleInflight    = "settle-inflight"
	FinalizerDecideMoney       = "decide-money"
	FinalizerSettleMoves       = "settle-moves"
	FinalizerPurgeOwned        = "purge-owned"

	StepAllocate  = "allocate"
	StepDirectory = "directory"
	StepDatabase  = "database"
	StepCerts     = "certs"
	StepCBN       = "cbn"
	StepSite      = "site"
	StepAPI       = "api"

	StatusVersion = 1
)

type Pos struct {
	File string
	Line int
	Col  int
}

type Spec struct {
	APIVersion string         `yaml:"apiVersion" json:"apiVersion"`
	Kind       string         `yaml:"kind" json:"kind"`
	Metadata   Metadata       `yaml:"metadata" json:"metadata"`
	Spec       Body           `yaml:"spec" json:"spec"`
	pos        map[string]Pos `yaml:"-" json:"-"`
}

func (s Spec) Pos(path string) (Pos, bool) {
	p, ok := s.pos[path]
	return p, ok
}

type Metadata struct {
	Name string `yaml:"name" json:"name"`
}

type Body struct {
	Start    string       `yaml:"start" json:"start"`
	NIP      NIP          `yaml:"nip,omitempty" json:"nip,omitempty"`
	Holidays Holidays     `yaml:"holidays,omitempty" json:"holidays,omitempty"`
	Connect  ConnectLegal `yaml:"connect,omitempty" json:"connect,omitempty"`
	Banks    []Bank       `yaml:"banks" json:"banks"`
	Partners []Partner    `yaml:"partners,omitempty" json:"partners,omitempty"`
}

type NIP struct {
	Sessions    int    `yaml:"sessions,omitempty" json:"sessions,omitempty"`
	TSQAfter    string `yaml:"tsq_after,omitempty" json:"tsq_after,omitempty"`
	TSQAttempts int    `yaml:"tsq_attempts,omitempty" json:"tsq_attempts,omitempty"`
}

type Holidays struct {
	Add    []string `yaml:"add,omitempty" json:"add,omitempty"`
	Remove []string `yaml:"remove,omitempty" json:"remove,omitempty"`
}

type ConnectLegal struct {
	LegalName  string `yaml:"legal_name,omitempty" json:"legal_name,omitempty"`
	BrandName  string `yaml:"brand_name,omitempty" json:"brand_name,omitempty"`
	RegistryID string `yaml:"registry_id,omitempty" json:"registry_id,omitempty"`
	CACNumber  string `yaml:"cac_number,omitempty" json:"cac_number,omitempty"`
}

type Bank struct {
	ID             string      `yaml:"id" json:"id"`
	Name           string      `yaml:"name" json:"name"`
	Type           string      `yaml:"type" json:"type"`
	NIPCode        string      `yaml:"nip_code,omitempty" json:"nip_code,omitempty"`
	NUBANCode      string      `yaml:"nuban_code,omitempty" json:"nuban_code,omitempty"`
	SettlesThrough string      `yaml:"settles_through,omitempty" json:"settles_through,omitempty"`
	State          string      `yaml:"state,omitempty" json:"state,omitempty"`
	NetDebitCap    int64       `yaml:"net_debit_cap_minor,omitempty" json:"net_debit_cap_minor,omitempty"`
	Collateral     int64       `yaml:"collateral_minor,omitempty" json:"collateral_minor,omitempty"`
	OpenBanking    OpenBanking `yaml:"open_banking,omitempty" json:"open_banking,omitempty"`
	OnRemove       *Removal    `yaml:"on_remove,omitempty" json:"on_remove,omitempty"`
}

type OpenBanking struct {
	ConsentMonths    int `yaml:"consent_months,omitempty" json:"consent_months,omitempty"`
	RefreshDays      int `yaml:"refresh_days,omitempty" json:"refresh_days,omitempty"`
	DormantDays      int `yaml:"dormant_days,omitempty" json:"dormant_days,omitempty"`
	AssertionSeconds int `yaml:"assertion_seconds,omitempty" json:"assertion_seconds,omitempty"`
}

type Removal struct {
	Money     string            `yaml:"money" json:"money"`
	To        string            `yaml:"to,omitempty" json:"to,omitempty"`
	Customers map[string]string `yaml:"customers,omitempty" json:"customers,omitempty"`
}

type Partner struct {
	ID           string   `yaml:"id" json:"id"`
	Name         string   `yaml:"name" json:"name"`
	LegalName    string   `yaml:"legal_name,omitempty" json:"legal_name,omitempty"`
	Purpose      string   `yaml:"purpose,omitempty" json:"purpose,omitempty"`
	RedirectURIs []string `yaml:"redirect_uris,omitempty" json:"redirect_uris,omitempty"`
	WebhookURL   string   `yaml:"webhook_url,omitempty" json:"webhook_url,omitempty"`
}

type Local struct {
	DatabaseURL string                  `yaml:"database_url"`
	Bind        string                  `yaml:"bind,omitempty"`
	Pace        float64                 `yaml:"pace,omitempty"`
	Ports       Ports                   `yaml:"ports,omitempty"`
	Connect     LocalConnect            `yaml:"connect,omitempty"`
	Partners    map[string]PartnerLocal `yaml:"partners,omitempty"`
}

type Ports struct {
	APIOffset int `yaml:"api_offset,omitempty"`
	Window    int `yaml:"window,omitempty"`
}

type LocalConnect struct {
	Host         string `yaml:"host,omitempty"`
	PartnerPort  int    `yaml:"partner_port,omitempty"`
	WidgetPort   int    `yaml:"widget_port,omitempty"`
	WidgetTLS    bool   `yaml:"widget_tls,omitempty"`
	ControlPort  int    `yaml:"control_port,omitempty"`
	HealthPort   int    `yaml:"health_port,omitempty"`
	SyncInterval string `yaml:"sync_interval,omitempty"`
	RefreshEvery string `yaml:"refresh_min_interval,omitempty"`
	PauseLease   string `yaml:"pause_lease,omitempty"`
}

type PartnerLocal struct {
	RedirectURIs []string `yaml:"redirect_uris,omitempty"`
	WebhookURL   string   `yaml:"webhook_url,omitempty"`
}

type Status struct {
	Version       int                 `json:"version"`
	Setup         string              `json:"setup"`
	SetupKey      uint64              `json:"setup_key,string"`
	Applied       Spec                `json:"applied"`
	AppliedSHA256 string              `json:"applied_sha256"`
	Resources     []Resource          `json:"resources"`
	Tombstones    []nibss.Institution `json:"tombstones,omitempty"`
}

type Resource struct {
	Kind                string            `json:"kind"`
	ID                  string            `json:"id"`
	Generation          int64             `json:"generation"`
	ObservedGeneration  int64             `json:"observed_generation"`
	Phase               string            `json:"phase"`
	DeletionRequestedAt *time.Time        `json:"deletion_requested_at,omitempty"`
	Finalizers          []string          `json:"finalizers,omitempty"`
	Done                []string          `json:"done,omitempty"`
	Money               *Removal          `json:"money,omitempty"`
	Allocated           Allocation        `json:"allocated"`
	Owned               []Owned           `json:"owned,omitempty"`
	Steps               map[string]string `json:"steps,omitempty"`
	Partner             *PartnerKeys      `json:"partner,omitempty"`
	Seeded              *Seeded           `json:"seeded,omitempty"`
	Result              json.RawMessage   `json:"result,omitempty"`
	Conditions          []Condition       `json:"conditions,omitempty"`
	Events              []Event           `json:"events,omitempty"`
	Attempts            int               `json:"attempts,omitempty"`
	NextAttemptAt       *time.Time        `json:"next_attempt_at,omitempty"`
}

type Allocation struct {
	NIPCode   string `json:"nip_code,omitempty"`
	NUBANCode string `json:"nuban_code,omitempty"`
	Database  string `json:"database,omitempty"`
	Port      int    `json:"port,omitempty"`
	APIPort   int    `json:"api_port,omitempty"`
}

type Owned struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type PartnerKeys struct {
	CertCN       string `json:"cert_cn"`
	APIKeySHA256 string `json:"api_key_sha256"`
	CertSHA256   string `json:"cert_sha256,omitempty"`
}

type Seeded struct {
	Template    string    `json:"template"`
	Value       int64     `json:"value"`
	HistoryDays int       `json:"history_days"`
	Generator   int       `json:"generator"`
	SeededAt    time.Time `json:"seeded_at"`
}

type Condition struct {
	Type      string    `json:"type"`
	Status    string    `json:"status"`
	Reason    string    `json:"reason"`
	Message   string    `json:"message"`
	ChangedAt time.Time `json:"changed_at"`
}

type Event struct {
	LastAt  time.Time `json:"last_at"`
	Reason  string    `json:"reason"`
	Message string    `json:"message"`
	Count   int       `json:"count"`
}

type FieldChange struct {
	Path string `json:"path"`
	From string `json:"from"`
	To   string `json:"to"`
}

type Impact struct {
	Lines       []string `json:"lines,omitempty"`
	Transfers   int      `json:"transfers"`
	NetMinor    int64    `json:"net_minor"`
	MoneyMinor  int64    `json:"money_minor"`
	Customers   int      `json:"customers"`
	Connections int      `json:"connections"`
	Databases   []string `json:"databases,omitempty"`
	Snapshots   []string `json:"snapshots,omitempty"`
}

type Action struct {
	Op          string        `json:"op"`
	Kind        string        `json:"kind"`
	ID          string        `json:"id"`
	Fields      []FieldChange `json:"fields,omitempty"`
	Allocated   []string      `json:"allocated,omitempty"`
	Destructive bool          `json:"destructive,omitempty"`
	Blockers    []string      `json:"blockers,omitempty"`
	Impact      *Impact       `json:"impact,omitempty"`
}

type Plan struct {
	Setup   string   `json:"setup"`
	Actions []Action `json:"actions"`
	Errors  []error  `json:"-"`
}
