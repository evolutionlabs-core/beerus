package nibss

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/nip"
)

const reportCursorSep = "|"

type Report struct {
	SessionID string    `json:"session_id"`
	CutoffAt  time.Time `json:"cutoff_at"`
	PostedAt  time.Time `json:"posted_at"`
	Outward   int64     `json:"outward_minor"`
	Inward    int64     `json:"inward_minor"`
	Net       int64     `json:"net_minor"`
	Transfers int       `json:"transfers"`
}

type reportRow struct {
	key      string
	cutoffAt time.Time
	postedAt time.Time
}

func (x Settlement) Position(code string) (outward, inward int64, err error) {
	for _, s := range x.Sessions {
		if s.FromCode == code {
			if outward > math.MaxInt64-s.Amount {
				return 0, 0, fmt.Errorf("settle %s at %s: the amount sent is too large to add", x.Key, code)
			}
			outward += s.Amount
		}
		if s.ToCode == code {
			if inward > math.MaxInt64-s.Amount {
				return 0, 0, fmt.Errorf("settle %s at %s: the amount received is too large to add", x.Key, code)
			}
			inward += s.Amount
		}
	}
	return outward, inward, nil
}

func (x Settlement) Report(code string, postedAt time.Time) (Report, error) {
	outward, inward, err := x.Position(code)
	if err != nil {
		return Report{}, err
	}
	net, ok := addNet(inward, -outward)
	if !ok {
		return Report{}, fmt.Errorf("settle %s at %s: the net is too large to add", x.Key, code)
	}
	transfers := 0
	for _, s := range x.Sessions {
		if s.FromCode == code || s.ToCode == code {
			transfers++
		}
	}
	return Report{SessionID: x.Key, CutoffAt: x.CutoffAt.In(clock.Lagos), PostedAt: postedAt.In(clock.Lagos), Outward: outward, Inward: inward, Net: net, Transfers: transfers}, nil
}

func encodeReportCursor(r reportRow) string {
	return base64.RawURLEncoding.EncodeToString([]byte(r.postedAt.UTC().Format(time.RFC3339Nano) + reportCursorSep + r.key))
}

func decodeReportCursor(cursor string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", &CursorError{Cursor: cursor}
	}
	at, key, ok := strings.Cut(string(raw), reportCursorSep)
	if !ok || key == "" {
		return time.Time{}, "", &CursorError{Cursor: cursor}
	}
	posted, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return time.Time{}, "", &CursorError{Cursor: cursor}
	}
	return posted, key, nil
}

func (s *Store) reportStart(ctx context.Context, after, cursor string) (time.Time, string, error) {
	if cursor != "" {
		return decodeReportCursor(cursor)
	}
	if after == "" {
		return time.Time{}, "", nil
	}
	var posted time.Time
	err := s.pool.QueryRow(ctx, `SELECT posted_at FROM settlement_sessions WHERE session_key = $1 AND posted_at IS NOT NULL`, after).Scan(&posted)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, "", nil
	}
	if err != nil {
		return time.Time{}, "", fmt.Errorf("read settlement session %s: %w", after, err)
	}
	return posted, after, nil
}

func (s *Store) SettlementReports(ctx context.Context, identity, after string, limit int, cursor string) ([]Report, string, error) {
	if limit < 1 || limit > maxPage {
		return nil, "", fmt.Errorf("limit must be from 1 to %d, not %d", maxPage, limit)
	}
	var code string
	err := s.pool.QueryRow(ctx, `SELECT nip_code FROM institutions WHERE certificate_identity = $1 AND status <> 'removed'`, identity).Scan(&code)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", fmt.Errorf("settlement reports for %s: %w", identity, nip.ErrUnknownInstitution)
	}
	if err != nil {
		return nil, "", fmt.Errorf("look up certificate identity %s: %w", identity, err)
	}
	postedAfter, keyAfter, err := s.reportStart(ctx, after, cursor)
	if err != nil {
		return nil, "", err
	}
	rows, err := s.pool.Query(ctx, `SELECT g.session_key, g.cutoff_at, g.posted_at FROM settlement_sessions g
		WHERE g.posted_at IS NOT NULL AND (g.posted_at, g.session_key) > ($2, $3)
		AND EXISTS (SELECT 1 FROM nip_sessions n WHERE n.settlement_session = g.session_key AND (n.from_code = $1 OR n.to_code = $1))
		ORDER BY g.posted_at, g.session_key LIMIT $4`, code, postedAfter, keyAfter, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("list settlement reports: %w", err)
	}
	var found []reportRow
	for rows.Next() {
		var r reportRow
		if err := rows.Scan(&r.key, &r.cutoffAt, &r.postedAt); err != nil {
			rows.Close()
			return nil, "", fmt.Errorf("read settlement report: %w", err)
		}
		found = append(found, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("list settlement reports: %w", err)
	}
	next := ""
	if len(found) > limit {
		found = found[:limit]
		next = encodeReportCursor(found[limit-1])
	}
	out, err := s.reports(ctx, code, found)
	if err != nil {
		return nil, "", err
	}
	return out, next, nil
}

func (s *Store) reports(ctx context.Context, code string, found []reportRow) ([]Report, error) {
	out := []Report{}
	if len(found) == 0 {
		return out, nil
	}
	keys := make([]string, 0, len(found))
	for _, r := range found {
		keys = append(keys, r.key)
	}
	list, err := s.sessions(ctx, "sessions in settlement reports", `SELECT `+sessionCols+` FROM nip_sessions WHERE settlement_session = ANY($1) ORDER BY resolved_at, session_id`, keys)
	if err != nil {
		return nil, err
	}
	groups := map[string][]Session{}
	for _, x := range list {
		groups[x.Settlement] = append(groups[x.Settlement], x)
	}
	for _, r := range found {
		report, err := Settlement{Key: r.key, CutoffAt: r.cutoffAt, Sessions: groups[r.key]}.Report(code, r.postedAt)
		if err != nil {
			return nil, err
		}
		out = append(out, report)
	}
	return out, nil
}
