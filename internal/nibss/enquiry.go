package nibss

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

func (s *Store) Callee(ctx context.Context, code, nuban string) (Member, error) {
	m, err := scanMember(s.pool.QueryRow(ctx, `SELECT `+memberCols+` FROM institutions WHERE nip_code = $1`, code))
	if errors.Is(err, pgx.ErrNoRows) {
		return Member{}, fmt.Errorf("bank code %s is %w", code, nip.ErrUnknownInstitution)
	}
	if err != nil {
		return Member{}, fmt.Errorf("look up institution %s: %w", code, err)
	}
	if !nubanRe.MatchString(nuban) {
		return m, fmt.Errorf("an account number has 10 digits: %w", nip.ErrInvalidAccount)
	}
	if m.Status != nip.StatusActive {
		return m, fmt.Errorf("%s is %s: %w", m.Name, m.Status, nip.ErrUnavailable)
	}
	if !world.ValidNUBAN(m.NUBANCode, nuban) {
		return m, fmt.Errorf("%s is not a valid account number at %s: %w", nuban, m.Name, nip.ErrInvalidAccount)
	}
	return m, nil
}
