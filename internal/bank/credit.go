package bank

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/nip"
)

const (
	answerCredited = "credited"
	answerRefused  = "refused"
	overflowCode   = nip.SystemError
)

type NIPCredit struct {
	SessionID    string    `json:"session_id"`
	FromCode     string    `json:"from_code"`
	FromBankName string    `json:"from_bank_name"`
	FromNUBAN    string    `json:"from_nuban"`
	FromName     string    `json:"from_name"`
	ToNUBAN      string    `json:"to_nuban"`
	Amount       int64     `json:"amount_minor"`
	Narration    string    `json:"narration"`
	CreatedAt    time.Time `json:"created_at"`
}

type CreditAnswer struct {
	Code     string
	Replayed bool
}

type FieldDiff struct {
	Field string
	First string
	Now   string
}

type SessionReusedError struct {
	SessionID string
	Fields    []FieldDiff
}

func (e *SessionReusedError) Error() string {
	if len(e.Fields) == 0 {
		return fmt.Sprintf("session %s reused with different details", e.SessionID)
	}
	names := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		names[i] = f.Field
	}
	return fmt.Sprintf("session %s reused with different %s", e.SessionID, strings.Join(names, ", "))
}

func (c NIPCredit) diff(first NIPCredit) []FieldDiff {
	var out []FieldDiff
	text := func(field, was, is string) {
		if was != is {
			out = append(out, FieldDiff{field, strconv.Quote(was), strconv.Quote(is)})
		}
	}
	text("from_code", first.FromCode, c.FromCode)
	text("from_bank_name", first.FromBankName, c.FromBankName)
	text("from_nuban", first.FromNUBAN, c.FromNUBAN)
	text("from_name", first.FromName, c.FromName)
	text("to_nuban", first.ToNUBAN, c.ToNUBAN)
	if first.Amount != c.Amount {
		out = append(out, FieldDiff{"amount_minor", strconv.FormatInt(first.Amount, 10), strconv.FormatInt(c.Amount, 10)})
	}
	text("narration", first.Narration, c.Narration)
	if !first.CreatedAt.Equal(c.CreatedAt) {
		out = append(out, FieldDiff{"created_at", first.CreatedAt.Format(time.RFC3339Nano), c.CreatedAt.Format(time.RFC3339Nano)})
	}
	return out
}

func refusalCode(err error) (string, bool) {
	switch {
	case errors.Is(err, ledger.ErrUnknownAccount), errors.Is(err, ledger.ErrAccountClosed):
		return nip.InvalidAccount, true
	case errors.Is(err, ledger.ErrAccountFrozen):
		return nip.Restricted, true
	case errors.Is(err, ledger.ErrOverflow):
		return overflowCode, true
	}
	return "", false
}

func (b *Bank) Credit(ctx context.Context, c NIPCredit, now time.Time) (CreditAnswer, error) {
	entry, err := nipInEntry(c.SessionID, c.ToNUBAN, c.Amount, Counterparty{Name: c.FromName, AccountNumber: c.FromNUBAN, BankCode: c.FromCode, BankName: c.FromBankName}, c.Narration, now)
	if err != nil {
		return CreditAnswer{}, fmt.Errorf("build credit for session %s: %w", c.SessionID, err)
	}
	request, err := json.Marshal(c)
	if err != nil {
		return CreditAnswer{}, fmt.Errorf("encode credit for session %s: %w", c.SessionID, err)
	}
	tx, err := b.L.Pool().Begin(ctx)
	if err != nil {
		return CreditAnswer{}, fmt.Errorf("begin credit for session %s: %w", c.SessionID, err)
	}
	defer tx.Rollback(ctx)
	var claimed string
	err = tx.QueryRow(ctx, `INSERT INTO nip_answers (session_id, answer, response_code, request, answered_at) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (session_id) DO NOTHING RETURNING session_id`, c.SessionID, answerCredited, nip.OK, request, now).Scan(&claimed)
	if errors.Is(err, pgx.ErrNoRows) {
		return b.priorCredit(ctx, tx, c)
	}
	if err != nil {
		return CreditAnswer{}, fmt.Errorf("claim session %s: %w", c.SessionID, err)
	}
	code, err := b.postCredit(ctx, tx, c, entry)
	if err != nil {
		return CreditAnswer{}, err
	}
	if code != nip.OK {
		if _, err := tx.Exec(ctx, `UPDATE nip_answers SET answer = $2, response_code = $3 WHERE session_id = $1`, c.SessionID, answerRefused, code); err != nil {
			return CreditAnswer{}, fmt.Errorf("record refusal of session %s: %w", c.SessionID, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return CreditAnswer{}, fmt.Errorf("commit credit for session %s: %w", c.SessionID, err)
	}
	return CreditAnswer{Code: code}, nil
}

func (b *Bank) postCredit(ctx context.Context, tx pgx.Tx, c NIPCredit, entry ledger.Entry) (string, error) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin posting for session %s: %w", c.SessionID, err)
	}
	_, err = b.L.PostTx(ctx, sp, entry)
	if err == nil {
		if err := sp.Commit(ctx); err != nil {
			return "", fmt.Errorf("release posting for session %s: %w", c.SessionID, err)
		}
		return nip.OK, nil
	}
	if rerr := sp.Rollback(ctx); rerr != nil {
		return "", fmt.Errorf("undo posting for session %s: %w", c.SessionID, errors.Join(err, rerr))
	}
	if errors.Is(err, ledger.ErrIdempotency) {
		return "", &SessionReusedError{SessionID: c.SessionID}
	}
	if code, ok := refusalCode(err); ok {
		return code, nil
	}
	return "", fmt.Errorf("post credit for session %s: %w", c.SessionID, err)
}

func (b *Bank) priorCredit(ctx context.Context, tx pgx.Tx, c NIPCredit) (CreditAnswer, error) {
	var code string
	var request []byte
	if err := tx.QueryRow(ctx, `SELECT response_code, request FROM nip_answers WHERE session_id = $1`, c.SessionID).Scan(&code, &request); err != nil {
		return CreditAnswer{}, fmt.Errorf("read session %s: %w", c.SessionID, err)
	}
	if request == nil {
		return CreditAnswer{Code: code}, nil
	}
	var first NIPCredit
	if err := json.Unmarshal(request, &first); err != nil {
		return CreditAnswer{}, fmt.Errorf("decode first credit of session %s: %w", c.SessionID, err)
	}
	if fields := c.diff(first); len(fields) > 0 {
		return CreditAnswer{}, &SessionReusedError{SessionID: c.SessionID, Fields: fields}
	}
	return CreditAnswer{Code: code, Replayed: true}, nil
}

func (b *Bank) CreditStatus(ctx context.Context, sessionID string, now time.Time) (string, error) {
	var posted bool
	if err := b.L.Pool().QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM journal_entries WHERE idempotency_key = $1)`, nipInKey(sessionID)).Scan(&posted); err != nil {
		return "", fmt.Errorf("find credit of session %s: %w", sessionID, err)
	}
	if posted {
		return nip.OK, nil
	}
	if _, err := b.L.Pool().Exec(ctx, `INSERT INTO nip_answers (session_id, answer, response_code, answered_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT (session_id) DO NOTHING`, sessionID, answerRefused, nip.NoRecord, now); err != nil {
		return "", fmt.Errorf("record no credit for session %s: %w", sessionID, err)
	}
	var answer string
	if err := b.L.Pool().QueryRow(ctx, `SELECT answer FROM nip_answers WHERE session_id = $1`, sessionID).Scan(&answer); err != nil {
		return "", fmt.Errorf("read session %s: %w", sessionID, err)
	}
	if answer == answerCredited {
		return nip.OK, nil
	}
	return nip.NoRecord, nil
}
