package nibss

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
)

const maxPage = 1000

func (s *Store) RecordConsent(ctx context.Context, e ConsentEvent) error {
	switch {
	case e.Bank == "" || e.ConsentID == "":
		return errors.New("a consent event needs a bank and a consent id")
	case e.ChangedAt.IsZero():
		return fmt.Errorf("consent %s event needs a changed time", e.ConsentID)
	case e.SourceSeq < 0:
		return fmt.Errorf("consent %s event: source seq %d is negative", e.ConsentID, e.SourceSeq)
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('consent_ledger:' || $1))`, e.Bank); err != nil {
			return fmt.Errorf("lock consent ledger for %s: %w", e.Bank, err)
		}
		_, err := tx.Exec(ctx, `
INSERT INTO consent_ledger (bank, consent_id, client_id, status, changed_at, source_seq)
SELECT $1::text, $2::text, $3::text, $4::text, $5::timestamptz, $6::bigint
WHERE $6 = 0 OR NOT EXISTS (SELECT 1 FROM consent_ledger WHERE bank = $1 AND source_seq >= $6)`,
			e.Bank, e.ConsentID, e.ClientID, e.Status, e.ChangedAt, e.SourceSeq)
		if err != nil {
			return fmt.Errorf("record consent %s: %w", e.ConsentID, err)
		}
		return nil
	})
}

func (s *Store) ConsentLedger(ctx context.Context, bank string, limit int, cursor string) ([]ConsentEvent, string, error) {
	if limit < 1 || limit > maxPage {
		return nil, "", fmt.Errorf("limit must be from 1 to %d, not %d", maxPage, limit)
	}
	var before int64
	if cursor != "" {
		n, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || n < 1 {
			return nil, "", fmt.Errorf("cursor %q is not valid; use the next value from the last page", cursor)
		}
		before = n
	}
	rows, err := s.pool.Query(ctx, `
SELECT seq, bank, consent_id, client_id, status, changed_at, source_seq FROM consent_ledger
WHERE ($1 = '' OR bank = $1) AND ($2 = 0 OR seq < $2)
ORDER BY seq DESC LIMIT $3`, bank, before, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("list consent ledger: %w", err)
	}
	defer rows.Close()
	out := []ConsentEvent{}
	for rows.Next() {
		var e ConsentEvent
		if err := rows.Scan(&e.Seq, &e.Bank, &e.ConsentID, &e.ClientID, &e.Status, &e.ChangedAt, &e.SourceSeq); err != nil {
			return nil, "", fmt.Errorf("read consent event: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("list consent ledger: %w", err)
	}
	if len(out) <= limit {
		return out, "", nil
	}
	out = out[:limit]
	return out, strconv.FormatInt(out[limit-1].Seq, 10), nil
}
