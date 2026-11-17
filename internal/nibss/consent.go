package nibss

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	maxPage      = 1000
	consentCols  = `seq, bank, incarnation, consent_id, client_id, partner_id, status, via, changed_at, source_seq`
	consentStamp = time.Microsecond
)

type ConsentReusedError struct {
	Bank      string
	SourceSeq int64
	Differs   []string
}

func (e *ConsentReusedError) Error() string {
	return fmt.Sprintf("source_seq %d of bank %s was first recorded with other %s", e.SourceSeq, e.Bank, strings.Join(e.Differs, " and "))
}

var consentStatuses = []string{"AwaitingAuthorisation", "Authorised", "Rejected", "Revoked", "Expired", "Dormant", "ReconsentRequired"}

type CursorError struct {
	Cursor string
}

func (e *CursorError) Error() string {
	return fmt.Sprintf("cursor %q is not valid; use the next value from the last page", e.Cursor)
}

func scanConsent(row pgx.Row) (ConsentEvent, error) {
	var e ConsentEvent
	err := row.Scan(&e.Seq, &e.Bank, &e.Incarnation, &e.ConsentID, &e.ClientID, &e.PartnerID, &e.Status, &e.Via, &e.ChangedAt, &e.SourceSeq)
	return e, err
}

func consentDiffers(have, want ConsentEvent) []string {
	var out []string
	for _, f := range []struct {
		name string
		same bool
	}{
		{"consent_id", have.ConsentID == want.ConsentID},
		{"client_id", have.ClientID == want.ClientID},
		{"partner_id", have.PartnerID == want.PartnerID},
		{"status", have.Status == want.Status},
		{"via", have.Via == want.Via},
		{"changed_at", have.ChangedAt.Equal(want.ChangedAt)},
	} {
		if !f.same {
			out = append(out, f.name)
		}
	}
	return out
}

func (s *Store) RecordConsent(ctx context.Context, e ConsentEvent) (ConsentEvent, bool, error) {
	switch {
	case e.Bank == "" || e.ConsentID == "":
		return ConsentEvent{}, false, invalid("a consent event needs a bank and a consent id")
	case e.Incarnation == "":
		return ConsentEvent{}, false, invalid("consent %s event needs an incarnation", e.ConsentID)
	case e.ChangedAt.IsZero():
		return ConsentEvent{}, false, invalid("consent %s event needs a changed time", e.ConsentID)
	case e.SourceSeq < 1:
		return ConsentEvent{}, false, invalid("consent %s event: source seq %d must be 1 or more", e.ConsentID, e.SourceSeq)
	case !slices.Contains(consentStatuses, e.Status):
		return ConsentEvent{}, false, invalid("consent %s event: status %q must be %s", e.ConsentID, e.Status, strings.Join(consentStatuses, ", "))
	}
	e.ChangedAt = e.ChangedAt.Truncate(consentStamp)
	var out ConsentEvent
	var replayed bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('consent_ledger:' || $1))`, e.Bank); err != nil {
			return fmt.Errorf("lock consent ledger for %s: %w", e.Bank, err)
		}
		first, err := scanConsent(tx.QueryRow(ctx, `SELECT `+consentCols+` FROM consent_ledger WHERE bank = $1 AND incarnation = $2 AND source_seq = $3`, e.Bank, e.Incarnation, e.SourceSeq))
		if err == nil {
			if d := consentDiffers(first, e); len(d) > 0 {
				return &ConsentReusedError{Bank: e.Bank, SourceSeq: e.SourceSeq, Differs: d}
			}
			out, replayed = first, true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("look up consent event %d: %w", e.SourceSeq, err)
		}
		out, err = scanConsent(tx.QueryRow(ctx, `
INSERT INTO consent_ledger (bank, incarnation, consent_id, client_id, partner_id, status, via, changed_at, source_seq)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING `+consentCols, e.Bank, e.Incarnation, e.ConsentID, e.ClientID, e.PartnerID, e.Status, e.Via, e.ChangedAt, e.SourceSeq))
		if err != nil {
			return fmt.Errorf("record consent %s: %w", e.ConsentID, err)
		}
		return nil
	})
	return out, replayed, err
}

func (s *Store) ConsentLedger(ctx context.Context, bank string, limit int, cursor string) ([]ConsentEvent, string, error) {
	if limit < 1 || limit > maxPage {
		return nil, "", fmt.Errorf("limit must be from 1 to %d, not %d", maxPage, limit)
	}
	var before int64
	if cursor != "" {
		n, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || n < 1 {
			return nil, "", &CursorError{Cursor: cursor}
		}
		before = n
	}
	rows, err := s.pool.Query(ctx, `
SELECT `+consentCols+` FROM consent_ledger
WHERE ($1 = '' OR bank = $1) AND ($2 = 0 OR seq < $2)
ORDER BY seq DESC LIMIT $3`, bank, before, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("list consent ledger: %w", err)
	}
	defer rows.Close()
	out := []ConsentEvent{}
	for rows.Next() {
		e, err := scanConsent(rows)
		if err != nil {
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
