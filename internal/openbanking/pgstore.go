package openbanking

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

type PGStore struct {
	Pool func() *pgxpool.Pool
}

const consentCols = `id, client_id, partner_id, partner_name, partner_legal, purpose, customer, status, scopes, accounts, transaction_from, analytics, created_at, authorised_at, expires_at, last_used_at, revoked_via`

func scanConsent(row pgx.Row) (*Consent, error) {
	var c Consent
	var status string
	var from *time.Time
	var auth, exp, used *time.Time
	if err := row.Scan(&c.ID, &c.ClientID, &c.PartnerID, &c.PartnerName, &c.PartnerLegal, &c.Purpose, &c.Customer, &status, &c.Scopes, &c.Accounts, &from, &c.Analytics, &c.CreatedAt, &auth, &exp, &used, &c.RevokedVia); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	c.Status = Status(status)
	if from != nil {
		c.TransactionFrom = clock.DateOf(*from)
	}
	if auth != nil {
		c.AuthorisedAt = *auth
	}
	if exp != nil {
		c.ExpiresAt = *exp
	}
	if used != nil {
		c.LastUsedAt = *used
	}
	return &c, nil
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func nullDate(d clock.Date) *string {
	if d.IsZero() {
		return nil
	}
	s := d.String()
	return &s
}

func (s *PGStore) CreateConsent(ctx context.Context, c *Consent) error {
	accounts, scopes := c.Accounts, c.Scopes
	if accounts == nil {
		accounts = []string{}
	}
	if scopes == nil {
		scopes = []string{}
	}
	_, err := s.Pool().Exec(ctx, `INSERT INTO ob_consents (`+consentCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::date,$12,$13,$14,$15,$16,$17)`,
		c.ID, c.ClientID, c.PartnerID, c.PartnerName, c.PartnerLegal, c.Purpose, c.Customer, string(c.Status), scopes, accounts, nullDate(c.TransactionFrom), c.Analytics, c.CreatedAt, nullTime(c.AuthorisedAt), nullTime(c.ExpiresAt), nullTime(c.LastUsedAt), c.RevokedVia)
	return err
}

func (s *PGStore) Consent(ctx context.Context, id string) (*Consent, error) {
	return scanConsent(s.Pool().QueryRow(ctx, `SELECT `+consentCols+` FROM ob_consents WHERE id = $1`, id))
}

func (s *PGStore) UpdateConsent(ctx context.Context, c *Consent) error {
	accounts, scopes := c.Accounts, c.Scopes
	if accounts == nil {
		accounts = []string{}
	}
	if scopes == nil {
		scopes = []string{}
	}
	tag, err := s.Pool().Exec(ctx, `UPDATE ob_consents SET customer=$2, status=$3, scopes=$4, accounts=$5, transaction_from=$6::date, analytics=$7, authorised_at=$8, expires_at=$9, last_used_at=$10, revoked_via=$11 WHERE id=$1`,
		c.ID, c.Customer, string(c.Status), scopes, accounts, nullDate(c.TransactionFrom), c.Analytics, nullTime(c.AuthorisedAt), nullTime(c.ExpiresAt), nullTime(c.LastUsedAt), c.RevokedVia)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *PGStore) ConsentsFor(ctx context.Context, customer string) ([]*Consent, error) {
	rows, err := s.Pool().Query(ctx, `SELECT `+consentCols+` FROM ob_consents WHERE customer = $1 ORDER BY created_at DESC`, customer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Consent{}
	for rows.Next() {
		c, err := scanConsent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *PGStore) PutToken(ctx context.Context, t *Token) error {
	_, err := s.Pool().Exec(ctx, `INSERT INTO ob_tokens (hash, kind, consent_id, client_id, thumbprint, family, parent, created_at, expires_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		t.Hash, t.Kind, t.ConsentID, t.ClientID, t.Thumbprint, t.Family, t.Parent, t.CreatedAt, t.ExpiresAt)
	return err
}

func (s *PGStore) Token(ctx context.Context, hash string) (*Token, error) {
	var t Token
	var rot, rev *time.Time
	err := s.Pool().QueryRow(ctx, `SELECT hash, kind, consent_id, client_id, thumbprint, family, parent, created_at, expires_at, rotated_at, revoked_at FROM ob_tokens WHERE hash = $1`, hash).
		Scan(&t.Hash, &t.Kind, &t.ConsentID, &t.ClientID, &t.Thumbprint, &t.Family, &t.Parent, &t.CreatedAt, &t.ExpiresAt, &rot, &rev)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if rot != nil {
		t.RotatedAt = *rot
	}
	t.Revoked = rev != nil
	return &t, nil
}

func (s *PGStore) MarkRotated(ctx context.Context, hash string, at time.Time) (bool, error) {
	tag, err := s.Pool().Exec(ctx, `UPDATE ob_tokens SET rotated_at = $2 WHERE hash = $1 AND rotated_at IS NULL`, hash, at)
	return tag.RowsAffected() == 1, err
}

func (s *PGStore) RevokeConsentTokens(ctx context.Context, id string) error {
	_, err := s.Pool().Exec(ctx, `UPDATE ob_tokens SET revoked_at = now() WHERE consent_id = $1 AND revoked_at IS NULL`, id)
	return err
}

func (s *PGStore) RevokeChildren(ctx context.Context, parent string) error {
	_, err := s.Pool().Exec(ctx, `UPDATE ob_tokens SET revoked_at = now() WHERE parent = $1 AND revoked_at IS NULL`, parent)
	return err
}

func (s *PGStore) RevokeFamily(ctx context.Context, family string) error {
	_, err := s.Pool().Exec(ctx, `UPDATE ob_tokens SET revoked_at = now() WHERE family = $1 AND revoked_at IS NULL`, family)
	return err
}

func (s *PGStore) RecordAccess(ctx context.Context, a Access) error {
	_, err := s.Pool().Exec(ctx, `INSERT INTO ob_access (consent_id, account, at) VALUES ($1,$2,$3)`, a.ConsentID, a.Account, a.At)
	return err
}

func (s *PGStore) AccessFor(ctx context.Context, customer string, limit int) ([]Access, error) {
	rows, err := s.Pool().Query(ctx, `SELECT a.consent_id, a.account, a.at FROM ob_access a JOIN ob_consents c ON c.id = a.consent_id WHERE c.customer = $1 ORDER BY a.id DESC LIMIT $2`, customer, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Access{}
	for rows.Next() {
		var a Access
		if err := rows.Scan(&a.ConsentID, &a.Account, &a.At); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *PGStore) Ping(ctx context.Context) error {
	return s.Pool().Ping(ctx)
}

func (s *PGStore) MarkUsed(ctx context.Context, id string, at time.Time) error {
	_, err := s.Pool().Exec(ctx, `UPDATE ob_consents SET last_used_at = $2 WHERE id = $1`, id, at)
	return err
}

func (s *PGStore) Awaiting(ctx context.Context) ([]*Consent, error) {
	rows, err := s.Pool().Query(ctx, `SELECT `+consentCols+` FROM ob_consents WHERE status = $1`, string(AwaitingAuthorisation))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Consent{}
	for rows.Next() {
		c, err := scanConsent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
