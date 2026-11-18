package nibss

import (
	"context"
	"fmt"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

const postGrace = time.Minute

const cutoffLayout = "2006-01-02 15:04"

func (s *Store) SettlementProblems(ctx context.Context, sched Schedule, now time.Time) ([]string, error) {
	out := []string{}
	rows, err := s.pool.Query(ctx, `SELECT session_key, cutoff_at FROM settlement_sessions WHERE posted_at IS NULL AND cutoff_at < $1 ORDER BY cutoff_at, session_key`, now.Add(-postGrace))
	if err != nil {
		return nil, fmt.Errorf("list unposted settlement sessions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var cutoff time.Time
		if err := rows.Scan(&key, &cutoff); err != nil {
			return nil, fmt.Errorf("read unposted settlement session: %w", err)
		}
		out = append(out, fmt.Sprintf("settlement session %s was cut off at %s and is not posted to the CBN yet", key, cutoff.In(clock.Lagos).Format(time.RFC3339)))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list unposted settlement sessions: %w", err)
	}
	cutoff := sched.Prev(now)
	if now.Sub(cutoff) <= postGrace {
		return out, nil
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM nip_sessions WHERE status = '00' AND settlement_session = '' AND resolved_at <= $1`, cutoff).Scan(&n); err != nil {
		return nil, fmt.Errorf("count unsettled transfers: %w", err)
	}
	switch {
	case n == 1:
		out = append(out, fmt.Sprintf("1 transfer resolved before the %s cutoff is not settled yet", cutoff.In(clock.Lagos).Format(cutoffLayout)))
	case n > 1:
		out = append(out, fmt.Sprintf("%d transfers resolved before the %s cutoff are not settled yet", n, cutoff.In(clock.Lagos).Format(cutoffLayout)))
	}
	return out, nil
}
