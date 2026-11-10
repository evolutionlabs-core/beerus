package nibss

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/text/unicode/norm"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/nip"
)

const (
	bvnSerialDigits = 7
	bvnSerialMax    = 9999999
)

func (s *Store) EnrolBVN(ctx context.Context, name, phone string, dob clock.Date) (string, error) {
	n, err := normaliseName(name)
	if err != nil {
		return "", err
	}
	p, err := s.NormalisePhone(phone)
	if err != nil {
		return "", err
	}
	if dob.IsZero() || clock.DateOf(dob.Start()) != dob {
		return "", fmt.Errorf("%w: date of birth %04d-%02d-%02d is not a real date", ErrBadDetails, dob.Y, int(dob.M), dob.D)
	}
	bvn, err := s.findBVN(ctx, n, p, dob)
	if err == nil || !errors.Is(err, pgx.ErrNoRows) {
		return bvn, err
	}
	var serial int64
	if err := s.pool.QueryRow(ctx, `SELECT nextval('bvn_seq')`).Scan(&serial); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "2200H" {
			return "", fmt.Errorf("all %d BVN serials are used", bvnSerialMax)
		}
		return "", fmt.Errorf("next BVN serial: %w", err)
	}
	bvn = makeBVN(serial)
	err = s.pool.QueryRow(ctx, `
INSERT INTO bvns (bvn, name, phone, dob) VALUES ($1, $2, $3, $4::date)
ON CONFLICT (name, phone, dob) DO NOTHING RETURNING bvn`, bvn, n, p, dob.String()).Scan(&bvn)
	if errors.Is(err, pgx.ErrNoRows) {
		bvn, err = s.findBVN(ctx, n, p, dob)
	}
	if err != nil {
		return "", fmt.Errorf("enrol BVN: %w", err)
	}
	return bvn, nil
}

func (s *Store) findBVN(ctx context.Context, name, phone string, dob clock.Date) (string, error) {
	var bvn string
	err := s.pool.QueryRow(ctx, `SELECT bvn FROM bvns WHERE name = $1 AND phone = $2 AND dob = $3::date`, name, phone, dob.String()).Scan(&bvn)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("look up BVN: %w", err)
	}
	return bvn, err
}

func makeBVN(serial int64) string {
	body := fmt.Sprintf("%s%0*d", nip.BVNPrefix, bvnSerialDigits, serial)
	return body + string(rune('0'+luhn(body)))
}

func luhn(digits string) int {
	sum := 0
	for i := len(digits) - 1; i >= 0; i-- {
		d := int(digits[i] - '0')
		if (len(digits)-1-i)%2 == 0 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return (10 - sum%10) % 10
}

func normaliseName(name string) (string, error) {
	var b strings.Builder
	for _, r := range norm.NFD.String(name) {
		switch {
		case unicode.Is(unicode.Mn, r), r == '\'', r == '’', r == '.':
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToUpper(r))
		default:
			b.WriteRune(' ')
		}
	}
	words := strings.Fields(b.String())
	if len(words) == 0 {
		return "", fmt.Errorf("%w: name %q has no letters", ErrBadDetails, name)
	}
	sort.Strings(words)
	return strings.Join(words, " "), nil
}

func (s *Store) NormalisePhone(phone string) (string, error) {
	return normalisePhone(phone, s.phone.CallingCode, s.phone.TrunkPrefix)
}

func normalisePhone(phone, callingCode, trunkPrefix string) (string, error) {
	var b strings.Builder
	for i, r := range strings.TrimSpace(phone) {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && i == 0:
		case r == ' ' || r == '-' || r == '(' || r == ')':
		default:
			return "", fmt.Errorf("%w: phone %q may hold only digits, spaces, dashes, brackets and a leading +", ErrBadDetails, phone)
		}
	}
	d := b.String()
	intl := strings.HasPrefix(strings.TrimSpace(phone), "+")
	switch {
	case intl && strings.HasPrefix(d, callingCode) && len(d) > len(callingCode):
		return d, nil
	case !intl && strings.HasPrefix(d, trunkPrefix) && len(d) > len(trunkPrefix):
		return callingCode + d[len(trunkPrefix):], nil
	case !intl && strings.HasPrefix(d, callingCode) && len(d) > len(callingCode):
		return d, nil
	}
	return "", fmt.Errorf("%w: phone %q must start with +%s or %s", ErrBadDetails, phone, callingCode, trunkPrefix)
}
