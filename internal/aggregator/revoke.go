package aggregator

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

func (a *App) RevokeAtBank(ctx context.Context, connID string) error {
	a.mu.Lock()
	c, ok := a.st.Connections[connID]
	if !ok {
		a.mu.Unlock()
		return errNotFound
	}
	bank, consent := c.Institution, c.ConsentID
	a.mu.Unlock()
	b := a.bank(bank)
	if b == nil || consent == "" {
		return errNotFound
	}
	return a.revokeRemote(ctx, b, consent)
}

func (a *App) revokeRemote(ctx context.Context, b *config.Bank, consent string) error {
	_, err := a.link.Do(ctx, b.URL(), http.MethodPost, "/internal/revoke", map[string]string{"consent_id": consent}, nil)
	var peer *control.PeerError
	if errors.As(err, &peer) && peer.Status == http.StatusNotFound {
		return openbanking.ErrNotFound
	}
	return err
}

func (a *App) RevokeBank(ctx context.Context, id string) (int, error) {
	ids := a.scopeIDs(func(c *Connection) bool { return c.Institution == id })
	var errs []error
	for _, cid := range ids {
		errs = append(errs, a.revokeConnection(ctx, cid))
	}
	return a.openAmong(ids), errors.Join(errs...)
}

func (a *App) RevokePartner(ctx context.Context, id string) (int, error) {
	ids, withConsent := a.markRevoked(func(c *Connection) bool { return c.PartnerID == id })
	err := a.revokeAtBanks(ctx, withConsent)
	return a.openAmong(ids), err
}

func (a *App) scopeIDs(inScope func(*Connection) bool) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	ids := []string{}
	for cid, c := range a.st.Connections {
		if inScope(c) {
			ids = append(ids, cid)
		}
	}
	sort.Strings(ids)
	return ids
}

func (a *App) revokeConnection(ctx context.Context, cid string) error {
	a.mu.Lock()
	c := a.st.Connections[cid]
	if c == nil {
		a.mu.Unlock()
		return nil
	}
	open := c.Status != "revoked" && c.Status != "expired"
	if open {
		for _, acct := range c.AccountIDs {
			a.emit(c.PartnerID, "account.removed", map[string]any{"connection_id": c.ID, "account_id": acct})
		}
	}
	atBank := c.ConsentID != "" && a.bank(c.Institution) != nil
	a.mu.Unlock()
	if atBank {
		if err := a.RevokeAtBank(ctx, cid); err != nil && !errors.Is(err, openbanking.ErrNotFound) {
			return fmt.Errorf("revoke connection %s at its bank: %w", cid, err)
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if c := a.st.Connections[cid]; c != nil && c.Status != "revoked" && c.Status != "expired" {
		a.setStatus(c, "revoked", "connection_revoked")
		a.vaultDrop(c.ID)
		a.dirty()
	}
	return nil
}

func (a *App) markRevoked(inScope func(*Connection) bool) ([]string, []string) {
	ids := a.scopeIDs(inScope)
	a.mu.Lock()
	defer a.mu.Unlock()
	var withConsent []string
	for _, cid := range ids {
		c := a.st.Connections[cid]
		if c.Status != "revoked" && c.Status != "expired" {
			for _, acct := range c.AccountIDs {
				a.emit(c.PartnerID, "account.removed", map[string]any{"connection_id": c.ID, "account_id": acct})
			}
			a.setStatus(c, "revoked", "connection_revoked")
			a.vaultDrop(c.ID)
		}
		if c.ConsentID != "" && a.bank(c.Institution) != nil {
			withConsent = append(withConsent, cid)
		}
	}
	a.dirty()
	return ids, withConsent
}

func (a *App) revokeAtBanks(ctx context.Context, withConsent []string) error {
	var errs []error
	for _, cid := range withConsent {
		if err := a.RevokeAtBank(ctx, cid); err != nil && !errors.Is(err, openbanking.ErrNotFound) {
			errs = append(errs, fmt.Errorf("revoke connection %s at its bank: %w", cid, err))
		}
	}
	return errors.Join(errs...)
}

func (a *App) openAmong(ids []string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, cid := range ids {
		if c := a.st.Connections[cid]; c != nil && c.Status != "revoked" && c.Status != "expired" {
			n++
		}
	}
	return n
}
