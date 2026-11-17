package bank

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type Send struct {
	SessionID string
	Request   []byte
	Attempts  int
	NextAt    time.Time
	CreatedAt time.Time
}

func insertSend(ctx context.Context, tx pgx.Tx, s NIPSend) error {
	if len(s.Request) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO nip_sends (session_id, request, next_tsq_at, created_at) VALUES ($1, $2, $3, $4) ON CONFLICT (session_id) DO NOTHING`, s.SessionID, s.Request, s.CheckAt, s.At); err != nil {
		return fmt.Errorf("record send %s: %w", s.SessionID, err)
	}
	return nil
}

func (b *Bank) NextSendSeq(ctx context.Context) (int64, error) {
	var seq int64
	if err := b.L.Pool().QueryRow(ctx, `SELECT nextval('nip_send_seq')`).Scan(&seq); err != nil {
		return 0, fmt.Errorf("number the send: %w", err)
	}
	return seq, nil
}

func (b *Bank) OpenSend(ctx context.Context, sessionID string) (Send, bool, error) {
	var s Send
	err := b.L.Pool().QueryRow(ctx, `SELECT session_id, request, attempts, next_tsq_at, created_at FROM nip_sends WHERE session_id = $1`, sessionID).
		Scan(&s.SessionID, &s.Request, &s.Attempts, &s.NextAt, &s.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Send{}, false, nil
	}
	if err != nil {
		return Send{}, false, fmt.Errorf("read send %s: %w", sessionID, err)
	}
	return s, true, nil
}

func (b *Bank) DueSends(ctx context.Context, at time.Time, limit int) ([]Send, error) {
	rows, err := b.L.Pool().Query(ctx, `SELECT session_id, request, attempts, next_tsq_at, created_at FROM nip_sends WHERE next_tsq_at <= $1 ORDER BY next_tsq_at, session_id LIMIT $2`, at, limit)
	if err != nil {
		return nil, fmt.Errorf("list due sends: %w", err)
	}
	defer rows.Close()
	out := []Send{}
	for rows.Next() {
		var s Send
		if err := rows.Scan(&s.SessionID, &s.Request, &s.Attempts, &s.NextAt, &s.CreatedAt); err != nil {
			return nil, fmt.Errorf("read due send: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list due sends: %w", err)
	}
	return out, nil
}

func (b *Bank) RescheduleSend(ctx context.Context, sessionID string, next time.Time) error {
	if _, err := b.L.Pool().Exec(ctx, `UPDATE nip_sends SET attempts = attempts + 1, next_tsq_at = $2 WHERE session_id = $1`, sessionID, next); err != nil {
		return fmt.Errorf("reschedule send %s: %w", sessionID, err)
	}
	return nil
}

func (b *Bank) FinishSend(ctx context.Context, sessionID string) error {
	if _, err := b.L.Pool().Exec(ctx, `DELETE FROM nip_sends WHERE session_id = $1`, sessionID); err != nil {
		return fmt.Errorf("finish send %s: %w", sessionID, err)
	}
	return nil
}

func (b *Bank) OpenSends(ctx context.Context) (int, error) {
	var n int
	if err := b.L.Pool().QueryRow(ctx, `SELECT count(*) FROM nip_sends`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count open sends: %w", err)
	}
	return n, nil
}
