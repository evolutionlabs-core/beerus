package nibss

import (
	"errors"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"time"
)

const (
	SessionPending = control.SessionPending
	SessionOK      = control.SessionOK
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

type ClosureSession struct {
	ID          string    `json:"session_id"`
	FromAccount string    `json:"from_account"`
	ToBankCode  string    `json:"to_bank_code"`
	ToAccount   string    `json:"to_account"`
	Amount      int64     `json:"amount_minor"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

type ConsentEvent struct {
	Seq         int64     `json:"seq"`
	Bank        string    `json:"bank"`
	Incarnation string    `json:"incarnation"`
	ConsentID   string    `json:"consent_id"`
	ClientID    string    `json:"client_id"`
	PartnerID   string    `json:"partner_id"`
	Status      string    `json:"status"`
	Via         string    `json:"via"`
	ChangedAt   time.Time `json:"changed_at"`
	SourceSeq   int64     `json:"source_seq"`
}
