package connect

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func (a *App) revokeBankConnections(ctx context.Context, r setup.Resource) (*setup.Condition, error) {
	id := r.ID
	read, ok := a.takeRevokeRead(id)
	if !ok {
		return a.blocked(setup.ReasonSettlementPending, "The connections of %s are being revoked at its bank. Try again soon.", id), nil
	}
	if read.err != nil {
		return nil, read.err
	}
	ids := a.scopeIDs(func(c *Connection) bool { return c.Institution == id })
	if !slices.Equal(ids, read.ids) {
		return nil, fmt.Errorf("the connections of %s changed while they were being revoked", id)
	}
	return nil, a.confirmRevoked(ids)
}

func (a *App) revokePartnerConnections(ctx context.Context, r setup.Resource) (*setup.Condition, error) {
	id := r.ID
	inScope := func(c *Connection) bool { return c.PartnerID == id }
	ids, withConsent := a.markRevoked(inScope)
	if err := a.revokeAtBanks(ctx, withConsent); err != nil {
		return nil, err
	}
	return nil, a.confirmRevoked(ids)
}

type revokeRead struct {
	at  time.Time
	ids []string
	err error
}

func (a *App) revokesDue(ctx context.Context) {
	st, err := a.ctl.status()
	if err != nil {
		a.log.Error("read control.json", "err", err)
		return
	}
	now := a.clock.Real()
	for _, r := range st.Resources {
		if r.Kind != setup.KindBank || (r.Phase != config.PhaseClosing && r.Phase != config.PhaseTerminating) || (r.NextAttemptAt != nil && now.Before(*r.NextAttemptAt)) || nextFinalizer(r) != setup.FinalizerRevokeConnections {
			continue
		}
		id := r.ID
		ids := a.scopeIDs(func(c *Connection) bool { return c.Institution == id })
		var errs []error
		for _, cid := range ids {
			errs = append(errs, a.revokeConnection(ctx, cid))
		}
		a.mu.Lock()
		if a.revokeReads == nil {
			a.revokeReads = map[string]revokeRead{}
		}
		a.revokeReads[id] = revokeRead{at: a.clock.Real(), ids: ids, err: errors.Join(errs...)}
		a.mu.Unlock()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, read := range a.revokeReads {
		if a.clock.Real().Sub(read.at) > finalizerReadTTL {
			delete(a.revokeReads, id)
		}
	}
}

func (a *App) takeRevokeRead(id string) (revokeRead, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	read, ok := a.revokeReads[id]
	delete(a.revokeReads, id)
	return read, ok && a.clock.Real().Sub(read.at) <= finalizerReadTTL
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
	atBank := c.ConsentID != "" && a.cfg.Bank(c.Institution) != nil
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
	a.mu.Lock()
	defer a.mu.Unlock()
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

func (a *App) confirmRevoked(ids []string) error {
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
