package nibss

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/evolutionlabs-core/beerus/internal/nip"
)

type SettlementPosition struct {
	Outward       int64 `json:"outward_minor"`
	Inward        int64 `json:"inward_minor"`
	PendingInward int64 `json:"pending_inward_minor"`
	Transfers     int   `json:"transfers"`
	Settlements   int   `json:"settlements"`
}

type OpenSessions struct {
	BankID    string `json:"bank_id"`
	Transfers int    `json:"transfers"`
	Outward   int64  `json:"outward_minor"`
	Inward    int64  `json:"inward_minor"`
	Unposted  int    `json:"unposted_settlements"`
}

const openRows = `(status = 'pending' OR (status = '00' AND settlement_session = '')) AND (from_code = $1 OR to_code = $1)`

const positionSQL = `WITH after_boundary AS (
		SELECT session_key FROM settlement_sessions WHERE posted_at IS NULL OR (posted_at, session_key) > ($2::timestamptz, $3::text)
	), counted AS (
		SELECT from_code, to_code, status, amount_minor FROM nip_sessions WHERE ` + openRows + `
		UNION ALL
		SELECT from_code, to_code, status, amount_minor FROM nip_sessions
		WHERE status = '00' AND settlement_session IN (SELECT session_key FROM after_boundary) AND (from_code = $1 OR to_code = $1)
	)
	SELECT
		coalesce(sum(amount_minor) FILTER (WHERE from_code = $1), 0),
		coalesce(sum(amount_minor) FILTER (WHERE to_code = $1 AND status = '00'), 0),
		coalesce(sum(amount_minor) FILTER (WHERE to_code = $1 AND status = 'pending'), 0),
		count(*),
		(SELECT count(*) FROM after_boundary g WHERE EXISTS (
			SELECT 1 FROM nip_sessions n WHERE n.settlement_session = g.session_key AND (n.from_code = $1 OR n.to_code = $1)))
	FROM counted`

const openSessionsSQL = `SELECT
		coalesce(sum(amount_minor) FILTER (WHERE from_code = $1), 0),
		coalesce(sum(amount_minor) FILTER (WHERE to_code = $1 AND status = '00'), 0),
		count(*),
		(SELECT count(*) FROM settlement_sessions g WHERE g.posted_at IS NULL AND EXISTS (
			SELECT 1 FROM nip_sessions n WHERE n.settlement_session = g.session_key AND (n.from_code = $1 OR n.to_code = $1)))
	FROM nip_sessions
	WHERE ` + openRows + `
`

type SumError struct {
	Msg string
}

func (e *SumError) Error() string { return e.Msg }

func minor(n pgtype.Numeric) (int64, bool) {
	v, err := n.Int64Value()
	return v.Int64, err == nil && v.Valid
}

func (s *Store) SettlementPosition(ctx context.Context, identity, after string) (SettlementPosition, error) {
	var code string
	err := s.pool.QueryRow(ctx, `SELECT nip_code FROM institutions WHERE certificate_identity = $1 AND status <> 'removed'`, identity).Scan(&code)
	if errors.Is(err, pgx.ErrNoRows) {
		return SettlementPosition{}, fmt.Errorf("settlement position for %s: %w", identity, nip.ErrUnknownInstitution)
	}
	if err != nil {
		return SettlementPosition{}, fmt.Errorf("look up certificate identity %s: %w", identity, err)
	}
	posted, key, err := s.reportStart(ctx, after, "")
	if err != nil {
		return SettlementPosition{}, err
	}
	var p SettlementPosition
	var outward, inward, pending pgtype.Numeric
	if err := s.pool.QueryRow(ctx, positionSQL, code, posted, key).Scan(&outward, &inward, &pending, &p.Transfers, &p.Settlements); err != nil {
		return SettlementPosition{}, fmt.Errorf("read settlement position of %s: %w", code, err)
	}
	var ok [3]bool
	p.Outward, ok[0] = minor(outward)
	p.Inward, ok[1] = minor(inward)
	p.PendingInward, ok[2] = minor(pending)
	if !ok[0] || !ok[1] || !ok[2] {
		return SettlementPosition{}, &SumError{Msg: fmt.Sprintf("the open sessions of %s add up to more than 64 bits", code)}
	}
	return p, nil
}

func (s *Store) OpenSessions(ctx context.Context, id string) (OpenSessions, error) {
	m, err := s.Member(ctx, id)
	if err != nil {
		return OpenSessions{}, err
	}
	o := OpenSessions{BankID: id}
	var outward, inward pgtype.Numeric
	if err := s.pool.QueryRow(ctx, openSessionsSQL, m.NIPCode).Scan(&outward, &inward, &o.Transfers, &o.Unposted); err != nil {
		return OpenSessions{}, fmt.Errorf("read open sessions of %s: %w", id, err)
	}
	var ok [2]bool
	o.Outward, ok[0] = minor(outward)
	o.Inward, ok[1] = minor(inward)
	if !ok[0] || !ok[1] {
		return OpenSessions{}, &SumError{Msg: fmt.Sprintf("the open sessions of %s add up to more than 64 bits", id)}
	}
	return o, nil
}
