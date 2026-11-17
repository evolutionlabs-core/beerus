package nibss

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

var ErrSessions = errors.New("nip_sessions changed")

type BankNet struct {
	BankID string `json:"bank_id"`
	Net    int64  `json:"net_minor"`
}

func (s *Store) KeepSchedule(ctx context.Context, sessions int) error {
	if _, err := s.pool.Exec(ctx, `INSERT INTO nip_schedule (sessions) VALUES ($1) ON CONFLICT DO NOTHING`, sessions); err != nil {
		return fmt.Errorf("keep nip_schedule: %w", err)
	}
	var kept int
	if err := s.pool.QueryRow(ctx, `SELECT sessions FROM nip_schedule`).Scan(&kept); err != nil {
		return fmt.Errorf("read nip_schedule: %w", err)
	}
	if kept != sessions {
		noun := "sessions"
		if kept == 1 {
			noun = "session"
		}
		return fmt.Errorf("%w: NIBSS has settled with %d %s a day, so nip_sessions must stay %d", ErrSessions, kept, noun, kept)
	}
	return nil
}

func (s *Store) Settle(ctx context.Context, sched Schedule, now time.Time, adhoc bool) ([]Settlement, error) {
	todo, err := s.Unposted(ctx)
	if err != nil {
		return nil, err
	}
	upTo := now
	if !adhoc {
		upTo = sched.Prev(now)
	}
	list, err := s.Unsettled(ctx, upTo)
	if err != nil {
		return nil, err
	}
	groups := map[time.Time][]Session{}
	for _, x := range list {
		c := upTo
		if !adhoc {
			c = sched.Next(x.ResolvedAt.Add(-time.Nanosecond))
		}
		groups[c] = append(groups[c], x)
	}
	for _, c := range slices.SortedFunc(maps.Keys(groups), func(x, y time.Time) int { return x.Compare(y) }) {
		ids := make([]string, 0, len(groups[c]))
		for _, x := range groups[c] {
			ids = append(ids, x.ID)
		}
		key := sched.Key(c)
		if adhoc {
			key = "adhoc:" + c.In(clock.Lagos).Format(time.RFC3339Nano) + ":" + ids[0]
		}
		if err := s.MarkSettled(ctx, ids, key, c); err != nil {
			return nil, err
		}
		todo = append(todo, Settlement{Key: key, CutoffAt: c, Sessions: groups[c]})
	}
	return todo, nil
}

func (s *Store) SettledExcept(ctx context.Context, code string, keys []string) ([]Settlement, error) {
	rows, err := s.pool.Query(ctx, `SELECT g.session_key, g.cutoff_at FROM settlement_sessions g
		WHERE g.posted_at IS NOT NULL AND NOT (g.session_key = ANY($2)) AND EXISTS (SELECT 1 FROM nip_sessions n WHERE n.settlement_session = g.session_key AND (n.from_code = $1 OR n.to_code = $1))
		ORDER BY g.cutoff_at, g.session_key`, code, keys)
	if err != nil {
		return nil, fmt.Errorf("list settlement sessions: %w", err)
	}
	out := []Settlement{}
	for rows.Next() {
		var x Settlement
		if err := rows.Scan(&x.Key, &x.CutoffAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read settlement session: %w", err)
		}
		out = append(out, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list settlement sessions: %w", err)
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

func (s *Store) PostedCount(ctx context.Context) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM settlement_sessions WHERE posted_at IS NOT NULL`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count posted settlement sessions: %w", err)
	}
	return n, nil
}

func (x Settlement) Nets(bankByCode map[string]string) ([]BankNet, error) {
	nets := map[string]int64{}
	for _, s := range x.Sessions {
		from, to := bankByCode[s.FromCode], bankByCode[s.ToCode]
		if from == "" || to == "" {
			return nil, fmt.Errorf("settle %s: session %s is between %s and %s, and one of them is not in the directory", x.Key, s.ID, s.FromCode, s.ToCode)
		}
		var ok bool
		if nets[to], ok = addNet(nets[to], s.Amount); !ok {
			return nil, fmt.Errorf("settle %s: the net of %s is too large to add", x.Key, to)
		}
		if nets[from], ok = addNet(nets[from], -s.Amount); !ok {
			return nil, fmt.Errorf("settle %s: the net of %s is too large to add", x.Key, from)
		}
	}
	out := make([]BankNet, 0, len(nets))
	for _, id := range slices.Sorted(maps.Keys(nets)) {
		out = append(out, BankNet{BankID: id, Net: nets[id]})
	}
	return out, nil
}

func Moved(nets []BankNet) []BankNet {
	return slices.DeleteFunc(slices.Clone(nets), func(n BankNet) bool { return n.Net == 0 })
}

func addNet(a, b int64) (int64, bool) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, false
	}
	return a + b, true
}
