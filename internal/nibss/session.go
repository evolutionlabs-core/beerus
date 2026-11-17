package nibss

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/nip"
)

var (
	nubanRe    = regexp.MustCompile(`^[0-9]{10}$`)
	resolvedRe = regexp.MustCompile(`^[0-9]{2}$`)
)

const sessionCols = `session_id, from_code, to_code, from_nuban, to_nuban, amount_minor, narration, status, outcome, tsq_attempts, next_tsq_at, created_at, resolved_at, settlement_session, closure`

func scanSession(row pgx.Row) (Session, error) {
	var x Session
	err := row.Scan(&x.ID, &x.FromCode, &x.ToCode, &x.FromNUBAN, &x.ToNUBAN, &x.Amount, &x.Narration, &x.Status, &x.Outcome, &x.TSQAttempts, &x.NextTSQAt, &x.CreatedAt, &x.ResolvedAt, &x.Settlement, &x.Closure)
	return x, err
}

func checkBegin(x Session) error {
	switch {
	case x.ID == "":
		return errors.New("a session needs an id")
	case !nubanRe.MatchString(x.FromNUBAN) || !nubanRe.MatchString(x.ToNUBAN):
		return fmt.Errorf("session %s: account numbers have 10 digits: %w", x.ID, nip.ErrInvalidAccount)
	case x.Amount <= 0:
		return fmt.Errorf("session %s: amount must be above zero, not %d", x.ID, x.Amount)
	case x.CreatedAt.IsZero():
		return fmt.Errorf("session %s needs a created time", x.ID)
	case x.Status != "" && x.Status != SessionPending:
		return fmt.Errorf("session %s starts pending, not %s", x.ID, x.Status)
	case !slices.Contains([]string{"", nip.OutcomeCredited, nip.OutcomeFailed, nip.OutcomeUnknown}, x.Outcome):
		return fmt.Errorf("session %s cannot start with outcome %s", x.ID, x.Outcome)
	case x.Outcome != "" && x.NextTSQAt == nil:
		return fmt.Errorf("session %s holds outcome %s and needs a status query time", x.ID, x.Outcome)
	case x.ResolvedAt != nil || x.Settlement != "" || x.TSQAttempts != 0:
		return fmt.Errorf("session %s must start unresolved, unsettled and with no status queries", x.ID)
	}
	return nil
}

type FieldDiff struct {
	Field string
	First string
	Now   string
}

type ReusedError struct {
	ID     string
	Fields []FieldDiff
}

func (e *ReusedError) Error() string {
	names := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		names[i] = f.Field
	}
	return fmt.Sprintf("session %s: session id reused with different %s", e.ID, strings.Join(names, ", "))
}

type ClosureError struct {
	Name   string
	Status string
}

func (e *ClosureError) Error() string {
	return fmt.Sprintf("%s is %s, so it cannot send a closure transfer", e.Name, e.Status)
}

func (s *Store) Begin(ctx context.Context, x Session) (Session, bool, error) {
	if err := checkBegin(x); err != nil {
		return Session{}, false, err
	}
	var out Session
	replayed := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		from, limit, err := lockSender(ctx, tx, x.FromCode)
		if err != nil {
			return err
		}
		stored, err := scanSession(tx.QueryRow(ctx, `SELECT `+sessionCols+` FROM nip_sessions WHERE session_id = $1`, x.ID))
		if err == nil {
			out, replayed = stored, true
			return checkSame(stored, x)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("look up session %s: %w", x.ID, err)
		}
		out, err = insertNew(ctx, tx, x, from, limit)
		return err
	})
	if err != nil {
		return Session{}, false, err
	}
	return out, replayed, nil
}

func lockSender(ctx context.Context, tx pgx.Tx, code string) (nip.Institution, *int64, error) {
	var from nip.Institution
	var limit *int64
	err := tx.QueryRow(ctx, `SELECT `+institutionCols+`, net_debit_cap_minor FROM institutions WHERE nip_code = $1 FOR UPDATE`, code).
		Scan(&from.ID, &from.Name, &from.NIPCode, &from.NUBANCode, &from.Type, &from.Status, &limit)
	if errors.Is(err, pgx.ErrNoRows) {
		return from, nil, fmt.Errorf("bank code %s is %w", code, nip.ErrUnknownInstitution)
	}
	if err != nil {
		return from, nil, fmt.Errorf("look up institution %s: %w", code, err)
	}
	return from, limit, nil
}

func insertNew(ctx context.Context, tx pgx.Tx, x Session, from nip.Institution, limit *int64) (Session, error) {
	to, err := institution(ctx, tx, x.ToCode, "")
	if err != nil {
		return Session{}, err
	}
	if x.Closure && from.Status != nip.StatusTerminating {
		return Session{}, &ClosureError{Name: from.Name, Status: from.Status}
	}
	if code, err := refusal(ctx, tx, x, from, to, limit); err != nil || code != "" {
		if err != nil {
			return Session{}, err
		}
		x.Status, x.Outcome, x.NextTSQAt = code, nip.OutcomeFailed, nil
		resolved := x.CreatedAt
		x.ResolvedAt = &resolved
	} else {
		x.Status = SessionPending
	}
	return scanSession(tx.QueryRow(ctx, `
INSERT INTO nip_sessions (session_id, from_code, to_code, from_nuban, to_nuban, amount_minor, narration, status, outcome, next_tsq_at, created_at, resolved_at, closure)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING `+sessionCols,
		x.ID, x.FromCode, x.ToCode, x.FromNUBAN, x.ToNUBAN, x.Amount, x.Narration, x.Status, x.Outcome, x.NextTSQAt, x.CreatedAt, x.ResolvedAt, x.Closure))
}

func refusal(ctx context.Context, tx pgx.Tx, x Session, from, to nip.Institution, limit *int64) (string, error) {
	if from.Status != nip.StatusActive && !x.Closure || to.Status != nip.StatusActive {
		return nip.Unavailable, nil
	}
	if x.Closure {
		return "", nil
	}
	if limit == nil {
		return "", fmt.Errorf("%s: %w", from.Name, ErrNoLimits)
	}
	p, err := position(ctx, tx, x.FromCode)
	if err != nil {
		return "", err
	}
	need, ok := addMinor(p.NetDebit(), x.Amount)
	if !ok || need > *limit {
		return nip.OverCap, nil
	}
	return "", nil
}

func addMinor(a, b int64) (int64, bool) {
	if b > math.MaxInt64-a {
		return 0, false
	}
	return a + b, true
}

func checkSame(stored, x Session) error {
	var diffs []FieldDiff
	text := func(field, first, now string) {
		if first != now {
			diffs = append(diffs, FieldDiff{field, strconv.Quote(first), strconv.Quote(now)})
		}
	}
	text("from_code", stored.FromCode, x.FromCode)
	text("to_code", stored.ToCode, x.ToCode)
	text("from_nuban", stored.FromNUBAN, x.FromNUBAN)
	text("to_nuban", stored.ToNUBAN, x.ToNUBAN)
	if stored.Amount != x.Amount {
		diffs = append(diffs, FieldDiff{"amount_minor", strconv.FormatInt(stored.Amount, 10), strconv.FormatInt(x.Amount, 10)})
	}
	text("narration", stored.Narration, x.Narration)
	if stored.Closure != x.Closure {
		diffs = append(diffs, FieldDiff{"closure", strconv.FormatBool(stored.Closure), strconv.FormatBool(x.Closure)})
	}
	if len(diffs) == 0 {
		return nil
	}
	return &ReusedError{ID: x.ID, Fields: diffs}
}

func checkResolve(id, status, outcome string, at time.Time) error {
	switch {
	case !resolvedRe.MatchString(status):
		return fmt.Errorf("session %s: %q is not a response code", id, status)
	case status == SessionOK && outcome != nip.OutcomeCredited:
		return fmt.Errorf("session %s: a 00 session is credited, not %q", id, outcome)
	case status != SessionOK && outcome != nip.OutcomeFailed:
		return fmt.Errorf("session %s: a %s session is failed, not %q", id, status, outcome)
	case at.IsZero():
		return fmt.Errorf("session %s needs a resolved time", id)
	}
	return nil
}

func (s *Store) Resolve(ctx context.Context, id, status, outcome string, at time.Time) error {
	if err := checkResolve(id, status, outcome, at); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var curStatus, curOutcome string
		var created time.Time
		err := tx.QueryRow(ctx, `SELECT status, outcome, created_at FROM nip_sessions WHERE session_id = $1 FOR UPDATE`, id).Scan(&curStatus, &curOutcome, &created)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("session %s: %w", id, nip.ErrNoRecord)
		}
		if err != nil {
			return fmt.Errorf("look up session %s: %w", id, err)
		}
		switch {
		case curStatus == status && curOutcome == outcome:
			return nil
		case curStatus == SessionPending:
			if (curOutcome == nip.OutcomeCredited && status != SessionOK) || (curOutcome == nip.OutcomeFailed && status == SessionOK) {
				return fmt.Errorf("session %s holds outcome %s and cannot resolve as %s: %w", id, curOutcome, status, ErrTransition)
			}
			if at.Before(created) {
				return fmt.Errorf("session %s cannot resolve at %s, before it began at %s", id, at.In(clock.Lagos).Format(time.RFC3339), created.In(clock.Lagos).Format(time.RFC3339))
			}
			_, err = tx.Exec(ctx, `UPDATE nip_sessions SET status = $2, outcome = $3, resolved_at = $4, next_tsq_at = NULL WHERE session_id = $1`, id, status, outcome, at)
		default:
			return fmt.Errorf("session %s is %s/%s and cannot become %s/%s: %w", id, curStatus, curOutcome, status, outcome, ErrTransition)
		}
		if err != nil {
			return fmt.Errorf("resolve session %s: %w", id, err)
		}
		return nil
	})
}

func (s *Store) TSQ(ctx context.Context, id string, next time.Time) (Session, error) {
	if next.IsZero() {
		return Session{}, fmt.Errorf("session %s needs the next status query time", id)
	}
	x, err := scanSession(s.pool.QueryRow(ctx, `
UPDATE nip_sessions SET tsq_attempts = tsq_attempts + 1, next_tsq_at = $2
WHERE session_id = $1 AND status = 'pending'
RETURNING `+sessionCols, id, next))
	if errors.Is(err, pgx.ErrNoRows) {
		x, err = scanSession(s.pool.QueryRow(ctx, `SELECT `+sessionCols+` FROM nip_sessions WHERE session_id = $1`, id))
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, fmt.Errorf("session %s: %w", id, nip.ErrNoRecord)
	}
	if err != nil {
		return Session{}, fmt.Errorf("query session %s: %w", id, err)
	}
	return x, nil
}

func (s *Store) NoAnswer(ctx context.Context, id string, next time.Time) (Session, error) {
	if next.IsZero() {
		return Session{}, fmt.Errorf("session %s needs the next status query time", id)
	}
	x, err := scanSession(s.pool.QueryRow(ctx, `
UPDATE nip_sessions SET outcome = $3, next_tsq_at = $2
WHERE session_id = $1 AND status = 'pending' AND outcome = ''
RETURNING `+sessionCols, id, next, nip.OutcomeUnknown))
	if errors.Is(err, pgx.ErrNoRows) {
		return s.Session(ctx, id)
	}
	if err != nil {
		return Session{}, fmt.Errorf("record no answer for session %s: %w", id, err)
	}
	return x, nil
}

func (s *Store) Session(ctx context.Context, id string) (Session, error) {
	x, err := scanSession(s.pool.QueryRow(ctx, `SELECT `+sessionCols+` FROM nip_sessions WHERE session_id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, fmt.Errorf("session %s: %w", id, nip.ErrNoRecord)
	}
	if err != nil {
		return Session{}, fmt.Errorf("look up session %s: %w", id, err)
	}
	return x, nil
}

func (s *Store) Unsettled(ctx context.Context, upTo time.Time) ([]Session, error) {
	return s.sessions(ctx, "unsettled sessions", `
SELECT `+sessionCols+` FROM nip_sessions
WHERE status = '00' AND settlement_session = '' AND resolved_at <= $1
ORDER BY resolved_at, session_id`, upTo)
}

func (s *Store) sessions(ctx context.Context, what, sql string, args ...any) ([]Session, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", what, err)
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		x, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", what, err)
		}
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list %s: %w", what, err)
	}
	return out, nil
}

func (s *Store) MarkSettled(ctx context.Context, ids []string, session string, cutoff time.Time) error {
	if session == "" {
		return errors.New("mark settled needs a settlement session")
	}
	want := slices.Clone(ids)
	slices.Sort(want)
	want = slices.Compact(want)
	if len(want) == 0 {
		return errors.New("mark settled needs at least one session id")
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT session_id, status, settlement_session FROM nip_sessions WHERE session_id = ANY($1) ORDER BY session_id FOR UPDATE`, want)
		if err != nil {
			return fmt.Errorf("look up sessions to settle: %w", err)
		}
		found, notOK, other := 0, 0, 0
		for rows.Next() {
			var id, status, settled string
			if err := rows.Scan(&id, &status, &settled); err != nil {
				rows.Close()
				return fmt.Errorf("read session to settle: %w", err)
			}
			found++
			switch {
			case status != SessionOK:
				notOK++
			case settled != "" && settled != session:
				other++
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("look up sessions to settle: %w", err)
		}
		switch {
		case found != len(want):
			return fmt.Errorf("%d of %d sessions to settle do not exist: %w", len(want)-found, len(want), nip.ErrNoRecord)
		case notOK > 0:
			return fmt.Errorf("%d of %d sessions to settle are not 00", notOK, len(want))
		case other > 0:
			return fmt.Errorf("%d of %d sessions are already settled in another session", other, len(want))
		}
		if _, err := tx.Exec(ctx, `INSERT INTO settlement_sessions (session_key, cutoff_at) VALUES ($1, $2) ON CONFLICT (session_key) DO NOTHING`, session, cutoff); err != nil {
			return fmt.Errorf("record settlement session %s: %w", session, err)
		}
		if _, err := tx.Exec(ctx, `UPDATE nip_sessions SET settlement_session = $2 WHERE session_id = ANY($1) AND settlement_session = ''`, want, session); err != nil {
			return fmt.Errorf("mark sessions settled: %w", err)
		}
		return nil
	})
}

type Settlement struct {
	Key      string
	CutoffAt time.Time
	Sessions []Session
}

func (s *Store) Unposted(ctx context.Context) ([]Settlement, error) {
	rows, err := s.pool.Query(ctx, `SELECT session_key, cutoff_at FROM settlement_sessions WHERE posted_at IS NULL ORDER BY cutoff_at, session_key`)
	if err != nil {
		return nil, fmt.Errorf("list unposted settlement sessions: %w", err)
	}
	out := []Settlement{}
	for rows.Next() {
		var x Settlement
		if err := rows.Scan(&x.Key, &x.CutoffAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read unposted settlement session: %w", err)
		}
		out = append(out, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list unposted settlement sessions: %w", err)
	}
	for i := range out {
		list, err := s.sessions(ctx, "sessions in settlement "+out[i].Key, `SELECT `+sessionCols+` FROM nip_sessions WHERE settlement_session = $1 ORDER BY resolved_at, session_id`, out[i].Key)
		if err != nil {
			return nil, err
		}
		out[i].Sessions = list
	}
	return out, nil
}

func (s *Store) MarkPosted(ctx context.Context, session string, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE settlement_sessions SET posted_at = coalesce(posted_at, $2) WHERE session_key = $1`, session, at)
	if err != nil {
		return fmt.Errorf("mark settlement %s posted: %w", session, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("settlement session %s: %w", session, nip.ErrNoRecord)
	}
	return nil
}

func (s *Store) ClosureSent(ctx context.Context, fromCode string) ([]Session, error) {
	return s.sessions(ctx, "closure sessions", `SELECT `+sessionCols+` FROM nip_sessions WHERE from_code = $1 AND closure AND status IN ('00', 'pending') ORDER BY created_at, session_id`, fromCode)
}

func (s *Store) Open(ctx context.Context) ([]Session, error) {
	return s.sessions(ctx, "open sessions", `
SELECT `+sessionCols+` FROM nip_sessions
WHERE status = 'pending' OR (status = '00' AND settlement_session = '')
ORDER BY created_at, session_id`)
}

func (s *Store) SettledNets(ctx context.Context) (map[string]map[string]int64, error) {
	rows, err := s.pool.Query(ctx, `
SELECT code, settlement_session, sum(net)::bigint FROM (
    SELECT to_code AS code, settlement_session, amount_minor AS net FROM nip_sessions WHERE settlement_session <> ''
    UNION ALL
    SELECT from_code, settlement_session, -amount_minor FROM nip_sessions WHERE settlement_session <> ''
) t GROUP BY 1, 2`)
	if err != nil {
		return nil, fmt.Errorf("read settled nets: %w", err)
	}
	defer rows.Close()
	out := map[string]map[string]int64{}
	for rows.Next() {
		var code, key string
		var net int64
		if err := rows.Scan(&code, &key, &net); err != nil {
			return nil, fmt.Errorf("read settled nets: %w", err)
		}
		if out[code] == nil {
			out[code] = map[string]int64{}
		}
		out[code][key] = net
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read settled nets: %w", err)
	}
	return out, nil
}
