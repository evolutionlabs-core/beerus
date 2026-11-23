package nibss

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

const (
	aggregatorRole = "aggregator"
	clientIDMax    = 40
)

var (
	ErrUnknownClient = errors.New("unknown registry client")

	clientIDRe = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9._-]{0," + strconv.Itoa(clientIDMax-1) + "}$")
)

const clientCols = `client_id, name, legal_name, registry_id, cac, role, cert_cn, jwk, redirect_uris, registered_at`

type RegistryConflictError struct {
	Message string
	Hint    string
}

func (e *RegistryConflictError) Error() string {
	return e.Message
}

func scanClient(row pgx.Row) (openbanking.RegistryClient, error) {
	var c openbanking.RegistryClient
	var jwk, uris []byte
	if err := row.Scan(&c.ID, &c.Name, &c.LegalName, &c.RegistryID, &c.CAC, &c.Role, &c.CertCN, &jwk, &uris, &c.RegisteredAt); err != nil {
		return c, err
	}
	if err := json.Unmarshal(jwk, &c.JWK); err != nil {
		return c, fmt.Errorf("read key of client %s: %w", c.ID, err)
	}
	if err := json.Unmarshal(uris, &c.RedirectURIs); err != nil {
		return c, fmt.Errorf("read redirect URIs of client %s: %w", c.ID, err)
	}
	return c, nil
}

func localHost(h string) bool {
	return h == "localhost" || strings.HasSuffix(h, ".localhost") || net.ParseIP(h).IsLoopback()
}

func checkClient(c openbanking.RegistryClient) error {
	if !clientIDRe.MatchString(c.ID) {
		return invalid("client id %q must be letters, digits, dots, dashes and underscores, up to %d characters", c.ID, clientIDMax)
	}
	if strings.TrimSpace(c.Name) == "" || strings.TrimSpace(c.LegalName) == "" {
		return invalid("client %s needs a name and a legal_name", c.ID)
	}
	if c.CertCN == "" {
		return invalid("client %s needs a cert_cn: the ID it sends in the Beerus-Caller header", c.ID)
	}
	if c.Role != aggregatorRole {
		return invalid("client %s role must be %s, not %q", c.ID, aggregatorRole, c.Role)
	}
	if _, err := c.JWK.Public(); err != nil {
		return invalid("client %s jwk is not valid: %v", c.ID, err)
	}
	if len(c.RedirectURIs) == 0 {
		return invalid("client %s needs at least one redirect URI", c.ID)
	}
	for _, u := range c.RedirectURIs {
		p, err := url.Parse(u)
		if err != nil || p.Host == "" || p.Fragment != "" || (p.Scheme != "https" && !(p.Scheme == "http" && localHost(p.Hostname()))) {
			return invalid("redirect URI %q must be an https URL, or an http URL on localhost", u)
		}
	}
	return nil
}

func clientDifferences(have, want openbanking.RegistryClient) (registered, requested []string) {
	for _, f := range []struct{ name, have, ask string }{
		{"cert_cn", have.CertCN, want.CertCN},
		{"role", have.Role, want.Role},
		{"cac", have.CAC, want.CAC},
		{"jwk", fmt.Sprintf("%s.%s", have.JWK.X, have.JWK.Y), fmt.Sprintf("%s.%s", want.JWK.X, want.JWK.Y)},
	} {
		if f.have != f.ask {
			registered = append(registered, f.name+" "+f.have)
			requested = append(requested, f.name+" "+f.ask)
		}
	}
	return registered, requested
}

func (s *Store) EnsureClient(ctx context.Context, c openbanking.RegistryClient, createOnly bool) (openbanking.RegistryClient, error) {
	c.RegistryID = c.ID
	if err := checkClient(c); err != nil {
		return openbanking.RegistryClient{}, err
	}
	jwk, err := json.Marshal(c.JWK)
	if err != nil {
		return openbanking.RegistryClient{}, fmt.Errorf("encode key of client %s: %w", c.ID, err)
	}
	uris, err := json.Marshal(c.RedirectURIs)
	if err != nil {
		return openbanking.RegistryClient{}, fmt.Errorf("encode redirect URIs of client %s: %w", c.ID, err)
	}
	var out openbanking.RegistryClient
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `LOCK TABLE registry_clients IN SHARE ROW EXCLUSIVE MODE`); err != nil {
			return fmt.Errorf("lock registry clients: %w", err)
		}
		cur, err := scanClient(tx.QueryRow(ctx, `SELECT `+clientCols+` FROM registry_clients WHERE client_id = $1`, c.ID))
		switch {
		case err == nil && createOnly:
			return &RegistryConflictError{Message: fmt.Sprintf("client %s is already registered", c.ID), Hint: "Send another client id."}
		case err == nil:
			registered, requested := clientDifferences(cur, c)
			if len(registered) > 0 {
				return &RegistryConflictError{Message: fmt.Sprintf("client %s is registered with %s, and this request has %s", c.ID, strings.Join(registered, " and "), strings.Join(requested, " and ")), Hint: "Send the registered details."}
			}
			out, err = scanClient(tx.QueryRow(ctx, `UPDATE registry_clients SET name = $2, legal_name = $3, redirect_uris = $4 WHERE client_id = $1 RETURNING `+clientCols, c.ID, c.Name, c.LegalName, uris))
			if err != nil {
				return fmt.Errorf("update client %s: %w", c.ID, err)
			}
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("look up client %s: %w", c.ID, err)
		}
		var holder string
		err = tx.QueryRow(ctx, `SELECT client_id FROM registry_clients WHERE cert_cn = $1`, c.CertCN).Scan(&holder)
		if err == nil {
			return &RegistryConflictError{Message: fmt.Sprintf("cert_cn %s already belongs to client %s", c.CertCN, holder), Hint: "Use another cert_cn."}
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("look up cert_cn %s: %w", c.CertCN, err)
		}
		out, err = scanClient(tx.QueryRow(ctx, `
INSERT INTO registry_clients (`+clientCols+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING `+clientCols, c.ID, c.Name, c.LegalName, c.RegistryID, c.CAC, c.Role, c.CertCN, jwk, uris, c.RegisteredAt))
		if err != nil {
			return fmt.Errorf("register client %s: %w", c.ID, err)
		}
		return nil
	})
	return out, err
}

func (s *Store) Client(ctx context.Context, id string) (openbanking.RegistryClient, error) {
	c, err := scanClient(s.pool.QueryRow(ctx, `SELECT `+clientCols+` FROM registry_clients WHERE client_id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, fmt.Errorf("client %s is %w", id, ErrUnknownClient)
	}
	if err != nil {
		return c, fmt.Errorf("look up client %s: %w", id, err)
	}
	return c, nil
}

func (s *Store) Clients(ctx context.Context, limit int, after string) ([]openbanking.RegistryClient, string, error) {
	if after != "" && !clientIDRe.MatchString(after) {
		return nil, "", &CursorError{Cursor: after}
	}
	rows, err := s.pool.Query(ctx, `SELECT `+clientCols+` FROM registry_clients WHERE client_id > $1 ORDER BY client_id LIMIT $2`, after, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("list registry clients: %w", err)
	}
	defer rows.Close()
	out := []openbanking.RegistryClient{}
	for rows.Next() {
		c, err := scanClient(rows)
		if err != nil {
			return nil, "", fmt.Errorf("read registry client: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("list registry clients: %w", err)
	}
	if len(out) <= limit {
		return out, "", nil
	}
	out = out[:limit]
	return out, out[limit-1].ID, nil
}
