package nibss

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/evolutionlabs-core/beerus/internal/nip"
)

var (
	ErrCodeTaken = errors.New("code or name already used")

	nipCodeRe   = regexp.MustCompile(`^[0-9]{6}$`)
	nubanCodeRe = regexp.MustCompile(`^([0-9]{3}|[0-9]{5}|[0-9]{6})$`)
)

type Member struct {
	nip.Institution
	CreditURL           string
	CertificateIdentity string
}

type RegistrationConflictError struct {
	ID         string
	Registered []string
	Requested  []string
}

func (e *RegistrationConflictError) Error() string {
	return fmt.Sprintf("bank %s is registered with %s, and this request has %s", e.ID, strings.Join(e.Registered, " and "), strings.Join(e.Requested, " and "))
}

type InvalidError struct {
	Message string
}

func (e *InvalidError) Error() string {
	return e.Message
}

func invalid(format string, args ...any) error {
	return &InvalidError{Message: fmt.Sprintf(format, args...)}
}

func checkRegister(in nip.Institution) error {
	switch {
	case in.ID == "" || strings.TrimSpace(in.Name) == "" || in.Type == "":
		return invalid("a bank needs an id, a name and a type")
	case !nipCodeRe.MatchString(in.NIPCode):
		return invalid("bank %s: nip_code %q must be 6 digits", in.ID, in.NIPCode)
	case !nubanCodeRe.MatchString(in.NUBANCode):
		return invalid("bank %s: nuban_code %q must be 3, 5 or 6 digits", in.ID, in.NUBANCode)
	case in.Status != "" && in.Status != nip.StatusActive:
		return invalid("bank %s registers as active, not %s; use SetStatus to change it", in.ID, in.Status)
	}
	return nil
}

func (s *Store) Register(ctx context.Context, m Member) error {
	in := m.Institution
	if err := checkRegister(in); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `LOCK TABLE institutions IN SHARE ROW EXCLUSIVE MODE`); err != nil {
			return fmt.Errorf("lock institutions: %w", err)
		}
		rows, err := tx.Query(ctx, `SELECT `+memberCols+` FROM institutions
WHERE id = $1 OR nip_code = $2 OR nuban_code = $3 OR (lower(name) = lower($4) AND status <> 'removed')
ORDER BY nip_code`, in.ID, in.NIPCode, in.NUBANCode, in.Name)
		if err != nil {
			return fmt.Errorf("look up bank %s: %w", in.ID, err)
		}
		var clash []Member
		for rows.Next() {
			c, err := scanMember(rows)
			if err != nil {
				rows.Close()
				return fmt.Errorf("read institution: %w", err)
			}
			clash = append(clash, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("look up bank %s: %w", in.ID, err)
		}
		var self *Member
		for i := range clash {
			c := &clash[i]
			if c.ID != in.ID {
				return clashError(in, c.Institution)
			}
			self = c
		}
		if self != nil {
			return updateMember(ctx, tx, m, *self)
		}
		_, err = tx.Exec(ctx, `INSERT INTO institutions (id, name, nip_code, nuban_code, type, status, credit_url, certificate_identity) VALUES ($1, $2, $3, $4, $5, 'active', $6, $7)`,
			in.ID, in.Name, in.NIPCode, in.NUBANCode, in.Type, m.CreditURL, m.CertificateIdentity)
		if err != nil {
			return fmt.Errorf("register bank %s: %w", in.ID, err)
		}
		return nil
	})
}

func differences(cur, want Member) (registered, requested []string) {
	for _, f := range []struct{ name, have, ask string }{
		{"nip_code", cur.NIPCode, want.NIPCode},
		{"nuban_code", cur.NUBANCode, want.NUBANCode},
		{"credit_url", cur.CreditURL, want.CreditURL},
		{"certificate_identity", cur.CertificateIdentity, want.CertificateIdentity},
	} {
		if f.have == f.ask || f.have == "" {
			continue
		}
		registered = append(registered, f.name+" "+f.have)
		requested = append(requested, f.name+" "+f.ask)
	}
	return registered, requested
}

func updateMember(ctx context.Context, tx pgx.Tx, m Member, cur Member) error {
	if cur.Status == nip.StatusRemoved {
		return fmt.Errorf("bank %s was removed, and its id stays reserved: %w", m.ID, ErrCodeTaken)
	}
	if registered, requested := differences(cur, m); len(registered) > 0 {
		return &RegistrationConflictError{ID: m.ID, Registered: registered, Requested: requested}
	}
	if cur.Name == m.Name && cur.Type == m.Type && cur.CreditURL == m.CreditURL && cur.CertificateIdentity == m.CertificateIdentity {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE institutions SET name = $2, type = $3, credit_url = $4, certificate_identity = $5, updated_at = now() WHERE id = $1`, m.ID, m.Name, m.Type, m.CreditURL, m.CertificateIdentity); err != nil {
		return fmt.Errorf("update bank %s: %w", m.ID, err)
	}
	return nil
}

func clashError(in, c nip.Institution) error {
	holder := c.ID
	if c.Status == nip.StatusRemoved {
		holder = "removed bank " + c.ID
	}
	switch {
	case c.NIPCode == in.NIPCode:
		return fmt.Errorf("bank %s: nip_code %s is held by %s: %w", in.ID, in.NIPCode, holder, ErrCodeTaken)
	case c.NUBANCode == in.NUBANCode:
		return fmt.Errorf("bank %s: nuban_code %s is held by %s: %w", in.ID, in.NUBANCode, holder, ErrCodeTaken)
	}
	return fmt.Errorf("bank %s: name %q is held by %s: %w", in.ID, in.Name, holder, ErrCodeTaken)
}
