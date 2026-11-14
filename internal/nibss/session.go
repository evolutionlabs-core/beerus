package nibss

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

var (
	errSessionReused = errors.New("session id reused with different details")

	nubanRe    = regexp.MustCompile(`^[0-9]{10}$`)
	resolvedRe = regexp.MustCompile(`^[0-9]{2}$`)
)

func (s *Store) NameEnquiry(ctx context.Context, code, nuban string) (string, error) {
	in, err := s.Institution(ctx, code)
	if err != nil {
		return "", err
	}
	if !nubanRe.MatchString(nuban) {
		return "", fmt.Errorf("an account number has 10 digits: %w", nip.ErrInvalidAccount)
	}
	if in.Status != StatusActive {
		return "", fmt.Errorf("%s is %s: %w", in.Name, in.Status, nip.ErrUnavailable)
	}
	if !world.ValidNUBAN(in.NUBANCode, nuban) {
		return "", fmt.Errorf("%s is not a valid account number at %s: %w", nuban, in.Name, nip.ErrInvalidAccount)
	}
	p := s.participant(in.ID)
	if p == nil {
		return "", fmt.Errorf("%s is offline: %w", in.Name, nip.ErrUnavailable)
	}
	name, err := p.NameEnquiry(ctx, nuban)
	if errors.Is(err, nip.ErrInvalidAccount) || errors.Is(err, nip.ErrNoRecord) || errors.Is(err, nip.ErrUnavailable) {
		return "", err
	}
	if err != nil {
		return "", fmt.Errorf("name enquiry at %s: %w", in.Name, err)
	}
	return name, nil
}

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
	case !slices.Contains([]string{"", OutcomeCredited, OutcomeFailed, OutcomeUnknown}, x.Outcome):
		return fmt.Errorf("session %s cannot start with outcome %s", x.ID, x.Outcome)
	case x.Outcome != "" && x.NextTSQAt == nil:
		return fmt.Errorf("session %s holds outcome %s and needs a status query time", x.ID, x.Outcome)
	case x.ResolvedAt != nil || x.Settlement != "" || x.TSQAttempts != 0:
		return fmt.Errorf("session %s must start unresolved, unsettled and with no status queries", x.ID)
	}
	return nil
}

func (s *Store) Begin(ctx context.Context, x Session) (Session, error) {
	if err := checkBegin(x); err != nil {
		return Session{}, err
	}
	var out Session
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		stored, err := scanSession(tx.QueryRow(ctx, `SELECT `+sessionCols+` FROM nip_sessions WHERE session_id = $1`, x.ID))
		if err == nil {
			if !sameRequest(stored, x) {
				return fmt.Errorf("session %s: %w", x.ID, errSessionReused)
			}
			out = stored
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("look up session %s: %w", x.ID, err)
		}
		from, err := sessionBank(ctx, tx, x.FromCode)
		if err != nil {
			return err
		}
		to, err := sessionBank(ctx, tx, x.ToCode)
		if err != nil {
			return err
		}
		if from.Status != StatusActive && !(x.Closure && from.Status == StatusTerminating) {
			return fmt.Errorf("%s is %s: %w", from.Name, from.Status, nip.ErrUnavailable)
		}
		if to.Status != StatusActive {
			return fmt.Errorf("%s is %s: %w", to.Name, to.Status, nip.ErrUnavailable)
		}
		x.Status = SessionPending
		out, err = scanSession(tx.QueryRow(ctx, `
INSERT INTO nip_sessions (session_id, from_code, to_code, from_nuban, to_nuban, amount_minor, narration, status, outcome, next_tsq_at, created_at, resolved_at, closure)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (session_id) DO NOTHING
RETURNING `+sessionCols,
			x.ID, x.FromCode, x.ToCode, x.FromNUBAN, x.ToNUBAN, x.Amount, x.Narration, x.Status, x.Outcome, x.NextTSQAt, x.CreatedAt, x.ResolvedAt, x.Closure))
		if errors.Is(err, pgx.ErrNoRows) {
			out, err = scanSession(tx.QueryRow(ctx, `SELECT `+sessionCols+` FROM nip_sessions WHERE session_id = $1`, x.ID))
			if err == nil && !sameRequest(out, x) {
				return fmt.Errorf("session %s: %w", x.ID, errSessionReused)
			}
		}
		if err != nil {
			return fmt.Errorf("record session %s: %w", x.ID, err)
		}
		return nil
	})
	if err != nil {
		return Session{}, err
	}
	return out, nil
}

func sessionBank(ctx context.Context, tx pgx.Tx, code string) (Institution, error) {
	return institution(ctx, tx, code, "FOR SHARE")
}

func sameRequest(a, b Session) bool {
	return a.FromCode == b.FromCode && a.ToCode == b.ToCode && a.FromNUBAN == b.FromNUBAN && a.ToNUBAN == b.ToNUBAN &&
		a.Amount == b.Amount && a.Narration == b.Narration && a.Closure == b.Closure
}

func checkResolve(id, status, outcome string, at time.Time) error {
	switch {
	case !resolvedRe.MatchString(status):
		return fmt.Errorf("session %s: %q is not a response code", id, status)
	case status == SessionOK && outcome != OutcomeCredited:
		return fmt.Errorf("session %s: a 00 session is credited, not %q", id, outcome)
	case status != SessionOK && outcome != OutcomeFailed && outcome != OutcomeReversed:
		return fmt.Errorf("session %s: a %s session is failed or reversed, not %q", id, status, outcome)
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
			if (curOutcome == OutcomeCredited && status != SessionOK) || (curOutcome == OutcomeFailed && status == SessionOK) {
				return fmt.Errorf("session %s holds outcome %s and cannot resolve as %s: %w", id, curOutcome, status, errTransition)
			}
			if at.Before(created) {
				return fmt.Errorf("session %s cannot resolve at %s, before it began at %s", id, at.In(clock.Lagos).Format(time.RFC3339), created.In(clock.Lagos).Format(time.RFC3339))
			}
			_, err = tx.Exec(ctx, `UPDATE nip_sessions SET status = $2, outcome = $3, resolved_at = $4, next_tsq_at = NULL WHERE session_id = $1`, id, status, outcome, at)
		case curStatus == status && curOutcome == OutcomeFailed && outcome == OutcomeReversed:
			_, err = tx.Exec(ctx, `UPDATE nip_sessions SET outcome = $2 WHERE session_id = $1`, id, outcome)
		default:
			return fmt.Errorf("session %s is %s/%s and cannot become %s/%s: %w", id, curStatus, curOutcome, status, outcome, errTransition)
		}
		if err != nil {
			return fmt.Errorf("resolve session %s: %w", id, err)
		}
		return nil
	})
}

func (s *Store) RevokeCredit(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE nip_sessions SET outcome = $2 WHERE session_id = $1 AND status = $3 AND outcome = $4`, id, OutcomeFailed, SessionPending, OutcomeCredited)
	if err != nil {
		return fmt.Errorf("revoke credit of session %s: %w", id, err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	x, err := s.Session(ctx, id)
	if err != nil {
		return err
	}
	if x.Outcome != OutcomeFailed {
		return fmt.Errorf("session %s is %s/%s, so its credit cannot be revoked: %w", id, x.Status, x.Outcome, errTransition)
	}
	return nil
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

func (s *Store) Due(ctx context.Context, at time.Time) ([]Session, error) {
	return s.sessions(ctx, "due sessions", `
SELECT `+sessionCols+` FROM nip_sessions
WHERE status = 'pending' AND next_tsq_at <= $1
ORDER BY next_tsq_at, created_at, session_id`, at)
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
