package nibss

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/evolutionlabs-core/beerus/internal/nip"
)

var ErrNoLimits = errors.New("no net debit cap set")

type Limits struct {
	NetDebitCap int64
	Collateral  int64
}

func (s *Store) SetLimits(ctx context.Context, id string, l Limits) error {
	if err := checkLimits(id, l); err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE institutions SET net_debit_cap_minor = $2, collateral_minor = $3, updated_at = now() WHERE id = $1`, id, l.NetDebitCap, l.Collateral)
	if err != nil {
		return fmt.Errorf("set limits of institution %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("bank id %s is %w", id, nip.ErrUnknownInstitution)
	}
	return nil
}

type Position struct {
	Outward int64
	Inward  int64
}

func (p Position) NetDebit() int64 {
	return max(p.Outward-p.Inward, 0)
}

func (s *Store) Position(ctx context.Context, code string) (Position, error) {
	return position(ctx, s.pool, code)
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
