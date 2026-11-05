package nibss

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

var (
	errCodeTaken = errors.New("code or name already used")

	nipCodeRe   = regexp.MustCompile(`^[0-9]{6}$`)
	nubanCodeRe = regexp.MustCompile(`^([0-9]{3}|[0-9]{6})$`)
)

func checkRegister(in Institution) error {
	switch {
	case in.Outside:
		return fmt.Errorf("register adds member banks; %s is marked outside", in.ID)
	case in.ID == "" || strings.TrimSpace(in.Name) == "" || in.Type == "":
		return errors.New("a member bank needs an id, a name and a type")
	case !nipCodeRe.MatchString(in.NIPCode):
		return fmt.Errorf("bank %s: nip_code %q must be 6 digits", in.ID, in.NIPCode)
	case !nubanCodeRe.MatchString(in.NUBANCode):
		return fmt.Errorf("bank %s: nuban_code %q must be 3 or 6 digits", in.ID, in.NUBANCode)
	case in.Status != "" && in.Status != StatusActive:
		return fmt.Errorf("bank %s registers as active, not %s; use SetStatus to change it", in.ID, in.Status)
	}
	return nil
}

func (s *Store) Register(ctx context.Context, in Institution) error {
	if err := checkRegister(in); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `LOCK TABLE institutions IN SHARE ROW EXCLUSIVE MODE`); err != nil {
			return fmt.Errorf("lock institutions: %w", err)
		}
		rows, err := tx.Query(ctx, `SELECT `+institutionCols+` FROM institutions
WHERE id = $1 OR nip_code = $2 OR nuban_code = $3 OR lower(name) = lower($4)
ORDER BY nip_code`, in.ID, in.NIPCode, in.NUBANCode, in.Name)
		if err != nil {
			return fmt.Errorf("look up bank %s: %w", in.ID, err)
		}
		var clash []Institution
		for rows.Next() {
			c, err := scanInstitution(rows)
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
		var self *Institution
		for i := range clash {
			c := &clash[i]
			if c.ID != in.ID {
				return clashError(in, *c)
			}
			self = c
		}
		if self != nil {
			return updateMember(ctx, tx, in, *self)
		}
		_, err = tx.Exec(ctx, `INSERT INTO institutions (id, name, nip_code, nuban_code, type, outside, status) VALUES ($1, $2, $3, $4, $5, false, 'active')`,
			in.ID, in.Name, in.NIPCode, in.NUBANCode, in.Type)
		if err != nil {
			return fmt.Errorf("register bank %s: %w", in.ID, err)
		}
		return nil
	})
}

func updateMember(ctx context.Context, tx pgx.Tx, in, cur Institution) error {
	switch {
	case cur.Status == StatusRemoved:
		return fmt.Errorf("bank %s was removed, and its id stays reserved: %w", in.ID, errCodeTaken)
	case cur.Outside:
		return fmt.Errorf("%s is the id of outside bank %s: %w", in.ID, cur.Name, errCodeTaken)
	case cur.NIPCode != in.NIPCode || cur.NUBANCode != in.NUBANCode:
		return fmt.Errorf("bank %s has codes %s/%s and they cannot change to %s/%s", in.ID, cur.NIPCode, cur.NUBANCode, in.NIPCode, in.NUBANCode)
	case cur.Name == in.Name && cur.Type == in.Type:
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE institutions SET name = $2, type = $3, updated_at = now() WHERE id = $1`, in.ID, in.Name, in.Type); err != nil {
		return fmt.Errorf("update bank %s: %w", in.ID, err)
	}
	return nil
}

func clashError(in, c Institution) error {
	holder := c.ID
	switch {
	case c.Status == StatusRemoved:
		holder = "removed bank " + c.ID
	case c.Outside:
		holder = "outside bank " + c.Name
	}
	switch {
	case c.NIPCode == in.NIPCode:
		return fmt.Errorf("bank %s: nip_code %s is held by %s: %w", in.ID, in.NIPCode, holder, errCodeTaken)
	case c.NUBANCode == in.NUBANCode:
		return fmt.Errorf("bank %s: nuban_code %s is held by %s: %w", in.ID, in.NUBANCode, holder, errCodeTaken)
	}
	return fmt.Errorf("bank %s: name %q is held by %s: %w", in.ID, in.Name, holder, errCodeTaken)
}
