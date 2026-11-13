package connect

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func (a *App) revokeBankConnections(_ context.Context, r setup.Resource) (*setup.Condition, error) {
	id := r.ID
	return nil, a.revokeConnections(func(c *Connection) bool { return c.Institution == id })
}

func (a *App) revokePartnerConnections(_ context.Context, r setup.Resource) (*setup.Condition, error) {
	id := r.ID
	return nil, a.revokeConnections(func(c *Connection) bool { return c.PartnerID == id })
}

func (a *App) revokeConnections(inScope func(*Connection) bool) error {
	a.mu.Lock()
	ids := make([]string, 0, len(a.st.Connections))
	for cid, c := range a.st.Connections {
		if inScope(c) {
			ids = append(ids, cid)
		}
	}
	sort.Strings(ids)
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
		if c.ConsentID != "" && a.cfg.Bank(c.Institution) != nil {
			withConsent = append(withConsent, cid)
		}
	}
	a.dirty()
	a.mu.Unlock()
	var errs []error
	for _, cid := range withConsent {
		if err := a.RevokeAtBank(cid); err != nil && !errors.Is(err, openbanking.ErrNotFound) {
			errs = append(errs, fmt.Errorf("revoke connection %s at its bank: %w", cid, err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, cid := range ids {
		if c := a.st.Connections[cid]; c == nil || (c.Status != "revoked" && c.Status != "expired") {
			return fmt.Errorf("connection %s is still %s after the revoke", cid, statusOf(c))
		}
	}
	return nil
}

func statusOf(c *Connection) string {
	if c == nil {
		return "missing"
	}
	return c.Status
}

func (a *App) revokedConnections(inScope func(*Connection) bool) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := []string{}
	for cid, c := range a.st.Connections {
		if inScope(c) && c.Status == "revoked" {
			out = append(out, cid)
		}
	}
	slices.Sort(out)
	return out
}
