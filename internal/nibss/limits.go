package nibss

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

var ErrNoLimits = errors.New("no net debit cap set")

type Limits struct {
	NetDebitCap int64
	Collateral  int64
}

type Position struct {
	Outward int64
	Inward  int64
}

func (p Position) NetDebit() int64 {
	return max(p.Outward-p.Inward, 0)
}

func position(ctx context.Context, q querier, code string) (Position, error) {
	var p Position
	err := q.QueryRow(ctx, `
SELECT coalesce(sum(amount_minor) FILTER (WHERE from_code = $1), 0)::bigint, coalesce(sum(amount_minor) FILTER (WHERE to_code = $1), 0)::bigint
FROM nip_sessions
WHERE status = 'pending' OR (status = '00' AND settlement_session = '')`, code).Scan(&p.Outward, &p.Inward)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Position{}, fmt.Errorf("add open sessions of %s: %w", code, err)
	}
	return p, nil
}
