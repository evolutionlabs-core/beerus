package control

import (
	"errors"
	"time"
)

type NIPSession struct {
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

var ErrNoSend = errors.New("no such send")
