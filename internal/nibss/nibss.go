package nibss

import (
	"context"
	"errors"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/nip"
)

const (
	SessionPending = "pending"
	SessionOK      = "00"

	OutcomeReversed = "reversed"
)

var (
	ErrBadDetails = errors.New("invalid BVN details")
)

type Session struct {
	ID          string     `json:"session_id"`
	FromCode    string     `json:"from_code"`
	ToCode      string     `json:"to_code"`
	FromNUBAN   string     `json:"from_nuban"`
	ToNUBAN     string     `json:"to_nuban"`
	Amount      int64      `json:"amount_minor"`
	Narration   string     `json:"narration"`
	Status      string     `json:"status"`
	Outcome     string     `json:"outcome,omitempty"`
	TSQAttempts int        `json:"tsq_attempts"`
	NextTSQAt   *time.Time `json:"next_tsq_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	ResolvedAt  *time.Time `json:"resolved_at,omitempty"`
	Settlement  string     `json:"settlement_session,omitempty"`
	Closure     bool       `json:"closure,omitempty"`
}

type ConsentEvent struct {
	Seq       int64     `json:"seq"`
	Bank      string    `json:"bank"`
	ConsentID string    `json:"consent_id"`
	ClientID  string    `json:"client_id"`
	Status    string    `json:"status"`
	ChangedAt time.Time `json:"changed_at"`
	SourceSeq int64     `json:"source_seq"`
}

type Switch interface {
	Directory(ctx context.Context) ([]nip.Institution, error)
	Institution(ctx context.Context, code string) (nip.Institution, error)
	Register(ctx context.Context, m Member) error
	SetStatus(ctx context.Context, id, status string) error
	EnrolBVN(ctx context.Context, name, phone string, dob clock.Date) (string, error)
	NameEnquiry(ctx context.Context, code, nuban string) (string, error)
	Begin(ctx context.Context, s Session) (Session, error)
	Resolve(ctx context.Context, id, status, outcome string, at time.Time) error
	TSQ(ctx context.Context, id string, next time.Time) (Session, error)
	Session(ctx context.Context, id string) (Session, error)
	Due(ctx context.Context, at time.Time) ([]Session, error)
	Unsettled(ctx context.Context, upTo time.Time) ([]Session, error)
	MarkSettled(ctx context.Context, ids []string, session string, cutoff time.Time) error
	Unposted(ctx context.Context) ([]Settlement, error)
	MarkPosted(ctx context.Context, session string, at time.Time) error
	Open(ctx context.Context) ([]Session, error)
	SettledNets(ctx context.Context) (map[string]map[string]int64, error)
	RecordConsent(ctx context.Context, e ConsentEvent) error
	ConsentLedger(ctx context.Context, bank string, limit int, cursor string) ([]ConsentEvent, string, error)
}
