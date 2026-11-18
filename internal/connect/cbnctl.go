package connect

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
)

const (
	cbnAccountPath = "/v1/control/banks/%s/settlement-account"
	cbnStatusPath  = "/v1/status"
	cbnAway        = "The CBN is not available right now. Try again soon."
)

type cbnAdmin struct {
	link server.Link
	base string
	port int
}

type cbnDownError struct {
	Port int
	Err  error
}

func (e *cbnDownError) Error() string {
	return fmt.Sprintf("the CBN is not answering on port %d: %v", e.Port, e.Err)
}

func (e *cbnDownError) Unwrap() error {
	return e.Err
}

type accountBody struct {
	Settlement int64 `json:"settlement_minor"`
	Vault      int64 `json:"vault_minor"`
}

func (c *cbnAdmin) openAccount(ctx context.Context, id string, settlement, vault int64) error {
	return c.call(ctx, http.MethodPut, fmt.Sprintf(cbnAccountPath, url.PathEscape(id)), accountBody{Settlement: settlement, Vault: vault}, nil)
}

func (c *cbnAdmin) problems(ctx context.Context) ([]string, error) {
	var out server.State
	if err := c.call(ctx, http.MethodGet, cbnStatusPath, nil, &out); err != nil {
		return nil, fmt.Errorf("read the status of the CBN: %w", err)
	}
	return out.Problems, nil
}

func (c *cbnAdmin) call(ctx context.Context, method, path string, in, out any) error {
	_, err := c.link.Do(ctx, c.base, method, path, in, out)
	var peer *server.PeerError
	if err == nil || errors.As(err, &peer) || ctx.Err() != nil {
		return err
	}
	if server.Unreachable(err) {
		return &cbnDownError{Port: c.port, Err: err}
	}
	return fmt.Errorf("call the CBN on port %d: %w", c.port, err)
}
