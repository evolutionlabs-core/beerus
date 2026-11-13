package connect

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/pki"
)

type LinkSession struct {
	ID            string        `json:"id"`
	PartnerID     string        `json:"partner_id"`
	WorkspaceID   string        `json:"workspace_id"`
	Status        string        `json:"status"`
	StartDate     clock.Date    `json:"start_date"`
	RedirectURI   string        `json:"redirect_uri"`
	State         string        `json:"state"`
	ConnectionID  string        `json:"connection_id"`
	TokenHash     string        `json:"token_hash"`
	TokenExpires  time.Time     `json:"token_expires"`
	TokenUsed     bool          `json:"token_used"`
	Institution   string        `json:"institution"`
	CustomerID    string        `json:"customer_id"`
	Accounts      []string      `json:"accounts"`
	Analytics     bool          `json:"analytics"`
	CodeHash      string        `json:"code_hash"`
	CodeExpires   time.Time     `json:"code_expires"`
	CodeUsed      bool          `json:"code_used"`
	CreatedAt     time.Time     `json:"created_at"`
	ExpiresReal   time.Time     `json:"expires_real"`
	OTP           string        `json:"otp"`
	OTPExpires    time.Time     `json:"otp_expires"`
	OTPAttempts   int           `json:"otp_attempts"`
	LoginAttempts int           `json:"login_attempts"`
	Verified      bool          `json:"verified"`
	BankState     string        `json:"bank_state,omitempty"`
	Verifier      string        `json:"verifier,omitempty"`
	BankConsentID string        `json:"bank_consent_id,omitempty"`
	ConsentEnds   time.Time     `json:"consent_ends,omitempty"`
	BankFrom      clock.Date    `json:"bank_from,omitempty"`
	BankAccounts  []bankAccount `json:"bank_accounts,omitempty"`
}

type WidgetSession struct {
	ID            string    `json:"id"`
	LinkSessionID string    `json:"link_session_id"`
	CSRF          string    `json:"csrf"`
	ExpiresReal   time.Time `json:"expires_real"`
}

type Connection struct {
	ID               string      `json:"id"`
	PartnerID        string      `json:"partner_id"`
	WorkspaceID      string      `json:"workspace_id"`
	Institution      string      `json:"institution"`
	CustomerID       string      `json:"customer_id"`
	Status           string      `json:"status"`
	StatusBeforeDown string      `json:"status_before_down"`
	StartDate        clock.Date  `json:"start_date"`
	EffectiveStart   *clock.Date `json:"effective_start"`
	AccountIDs       []string    `json:"account_ids"`
	ConsentID        string      `json:"consent_id"`
	AuthorisedAt     time.Time   `json:"authorised_at"`
	ExpiresAt        time.Time   `json:"expires_at"`
	LastSyncedAt     *time.Time  `json:"last_synced_at"`
	LastSyncedClock  time.Time   `json:"last_synced_clock"`
	LastSyncedReal   time.Time   `json:"last_synced_real"`
	CreatedAt        time.Time   `json:"created_at"`
	SyncDueReal      time.Time   `json:"sync_due_real"`
	FullDue          bool        `json:"full_due,omitempty"`
}

type AccountRec struct {
	ID           string     `json:"id"`
	PartnerID    string     `json:"partner_id"`
	Key          string     `json:"key"`
	ConnectionID string     `json:"connection_id"`
	From         clock.Date `json:"from"`
	Cut          bool       `json:"cut"`
	Ledger       int64      `json:"ledger"`
	Available    int64      `json:"available"`
	AsAt         time.Time  `json:"as_at"`
	LastRefresh  time.Time  `json:"last_refresh_real"`
	Removed      bool       `json:"removed"`
	HistoryDone  bool       `json:"history_done"`
	Grants       []string   `json:"grants"`
	NUBAN        string     `json:"nuban,omitempty"`
	Name         string     `json:"name,omitempty"`
	Type         string     `json:"type,omitempty"`
	Currency     string     `json:"currency,omitempty"`
}

type Txn struct {
	ID                   string        `json:"id"`
	AccountID            string        `json:"account_id"`
	Sequence             int64         `json:"sequence"`
	AmountMinor          int64         `json:"amount_minor"`
	Currency             string        `json:"currency"`
	Status               string        `json:"status"`
	PendingTransactionID *string       `json:"pending_transaction_id"`
	BookedAt             *string       `json:"booked_at"`
	ValueDate            string        `json:"value_date"`
	DescriptionRaw       string        `json:"description_raw"`
	Description          string        `json:"description"`
	Counterparty         *Counterparty `json:"counterparty"`
	References           References    `json:"references"`
	Channel              string        `json:"channel"`
	BalanceAfterMinor    *int64        `json:"balance_after_minor"`
}

type Counterparty struct {
	Name          *string `json:"name"`
	AccountNumber *string `json:"account_number"`
	BankCode      *string `json:"bank_code"`
	BankName      *string `json:"bank_name"`
}

type References struct {
	SessionID     *string `json:"session_id"`
	BankReference *string `json:"bank_reference"`
}

type ModifiedTxn struct {
	Txn
	ModificationReason string `json:"modification_reason"`
}

type LogEntry struct {
	Seq    int64  `json:"seq"`
	Kind   string `json:"kind"`
	Txn    Txn    `json:"txn"`
	Reason string `json:"reason,omitempty"`
}

type Feed struct {
	Lines map[string]Txn  `json:"lines"`
	Gone  map[string]bool `json:"gone"`
	Log   []LogEntry      `json:"log"`
}

type Refresh struct {
	ID          string    `json:"id"`
	AccountID   string    `json:"account_id"`
	Status      string    `json:"status"`
	RequestedAt time.Time `json:"requested_at"`
	DoneReal    time.Time `json:"done_real"`
}

type Delivery struct {
	ID          string          `json:"id"`
	PartnerID   string          `json:"partner_id"`
	Type        string          `json:"type"`
	Body        json.RawMessage `json:"body"`
	Status      string          `json:"status"`
	Attempts    int             `json:"attempts"`
	NextReal    time.Time       `json:"next_attempt_real"`
	LastStatus  int             `json:"last_status"`
	LastError   string          `json:"last_error"`
	CreatedReal time.Time       `json:"created_real"`
}

type IdemRec struct {
	Hash        string    `json:"hash"`
	Status      int       `json:"status"`
	ContentType string    `json:"content_type"`
	Body        []byte    `json:"body"`
	CreatedReal time.Time `json:"created_real"`
}

type AccessEvent struct {
	ConnectionID string    `json:"connection_id"`
	PartnerID    string    `json:"partner_id"`
	AccountID    string    `json:"account_id"`
	Key          string    `json:"key"`
	At           time.Time `json:"at"`
}

type RegistryClient struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	LegalName    string          `json:"legal_name"`
	RegistryID   string          `json:"registry_id"`
	CAC          string          `json:"cac"`
	Role         string          `json:"role"`
	CertCN       string          `json:"cert_cn"`
	JWK          openbanking.JWK `json:"jwk"`
	RedirectURIs []string        `json:"redirect_uris"`
	RegisteredAt time.Time       `json:"registered_at"`
}

type ConsentRecord struct {
	Seq       int64     `json:"seq"`
	SourceSeq int64     `json:"source_seq,omitempty"`
	ConsentID string    `json:"consent_id"`
	Bank      string    `json:"bank"`
	ClientID  string    `json:"client_id"`
	PartnerID string    `json:"partner_id"`
	Status    string    `json:"status"`
	Via       string    `json:"via"`
	At        time.Time `json:"at"`
}

type PendingRevoke struct {
	Bank      string    `json:"bank"`
	ConsentID string    `json:"consent_id"`
	Attempts  int       `json:"attempts"`
	Next      time.Time `json:"next"`
}

type State struct {
	Revokes        []PendingRevoke            `json:"revokes,omitempty"`
	SettleNow      []string                   `json:"settle_now,omitempty"`
	Registry       map[string]*RegistryClient `json:"registry"`
	ConsentLedger  []ConsentRecord            `json:"consent_ledger"`
	Access         []AccessEvent              `json:"access"`
	WorldID        string                     `json:"world_id"`
	Faults         []*Fault                   `json:"faults"`
	LinkSessions   map[string]*LinkSession    `json:"link_sessions"`
	WidgetSessions map[string]*WidgetSession  `json:"widget_sessions"`
	Connections    map[string]*Connection     `json:"connections"`
	Accounts       map[string]*AccountRec     `json:"accounts"`
	Feeds          map[string]*Feed           `json:"feeds"`
	Refreshes      map[string]*Refresh        `json:"refreshes"`
	Deliveries     []*Delivery                `json:"deliveries"`
	Idem           map[string]*IdemRec        `json:"idempotency"`
	Seq            int64                      `json:"seq"`
	Vault          map[string]*VaultEntry     `json:"vault,omitempty"`
	NIP            *NIPState                  `json:"nip,omitempty"`
}

func newState() *State {
	return &State{
		WorldID:        newID("wld"),
		LinkSessions:   map[string]*LinkSession{},
		WidgetSessions: map[string]*WidgetSession{},
		Connections:    map[string]*Connection{},
		Accounts:       map[string]*AccountRec{},
		Feeds:          map[string]*Feed{},
		Refreshes:      map[string]*Refresh{},
		Idem:           map[string]*IdemRec{},
		Registry:       map[string]*RegistryClient{},
		Vault:          map[string]*VaultEntry{},
		NIP:            &NIPState{},
	}
}

func (s *State) fill() {
	if s.LinkSessions == nil {
		s.LinkSessions = map[string]*LinkSession{}
	}
	if s.WidgetSessions == nil {
		s.WidgetSessions = map[string]*WidgetSession{}
	}
	if s.Connections == nil {
		s.Connections = map[string]*Connection{}
	}
	if s.Accounts == nil {
		s.Accounts = map[string]*AccountRec{}
	}
	if s.Feeds == nil {
		s.Feeds = map[string]*Feed{}
	}
	if s.Refreshes == nil {
		s.Refreshes = map[string]*Refresh{}
	}
	if s.Idem == nil {
		s.Idem = map[string]*IdemRec{}
	}
	if s.Registry == nil {
		s.Registry = map[string]*RegistryClient{}
	}
	if s.Vault == nil {
		s.Vault = map[string]*VaultEntry{}
	}
	if s.NIP == nil {
		s.NIP = &NIPState{}
	}
	for _, a := range s.Accounts {
		if len(a.Grants) == 0 && a.ConnectionID != "" && !a.Removed {
			a.Grants = []string{a.ConnectionID}
		}
	}
	for _, f := range s.Feeds {
		if f.Lines == nil {
			f.Lines = map[string]Txn{}
		}
		if f.Gone == nil {
			f.Gone = map[string]bool{}
		}
	}
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
	s.fill()
	return s, nil
}

func saveJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create directory for %s: %w", path, err)
	}
	return pki.WriteFile(path, b, 0o600)
}

var idEncoding = base32.NewEncoding("0123456789abcdefghjkmnpqrstvwxyz").WithPadding(base32.NoPadding)

func newID(prefix string) string {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + "_" + strings.ToLower(idEncoding.EncodeToString(b))
}

func newSecret(prefix string, n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + strings.ToLower(idEncoding.EncodeToString(b))
}
