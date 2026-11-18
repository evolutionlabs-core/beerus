package connect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/state"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

const snapshotInfix = "_snap_"

type PurgeImpact struct {
	Bank        BankStatus `json:"bank"`
	Database    string     `json:"database"`
	Connections int        `json:"connections"`
	Blockers    []string   `json:"blockers"`
}

func (a *App) PurgeImpact(ctx context.Context, id string) (PurgeImpact, error) {
	st, ok := a.bankStatus(id)
	banks := a.bankList()
	b := findBank(banks, id)
	if !ok || b == nil {
		return PurgeImpact{}, errNotFound
	}
	out := PurgeImpact{Bank: st, Database: b.Database, Blockers: []string{}}
	for _, other := range banks {
		if other.SettlesThrough == id {
			out.Blockers = append(out.Blockers, other.Name+" settles through this bank; purge it first")
		}
	}
	sends, err := a.openSends(ctx, b)
	if err != nil {
		return PurgeImpact{}, err
	}
	waiting, err := a.inflightWith(ctx, id, sends)
	if err != nil {
		return PurgeImpact{}, err
	}
	if waiting != "" {
		out.Blockers = append(out.Blockers, waiting)
	}
	a.mu.Lock()
	for _, c := range a.st.Connections {
		if c.Institution == id {
			out.Connections++
		}
	}
	a.mu.Unlock()
	return out, nil
}

const stepRetired = "retired"

func (a *App) PurgeBank(ctx context.Context, id string, settleNow bool) (setup.Resource, error) {
	err := a.applyEdit(ctx, func(sp *setup.Spec) error {
		i := slices.IndexFunc(sp.Spec.Banks, func(b setup.Bank) bool { return b.ID == id })
		if i < 0 {
			return errNotFound
		}
		sp.Spec.Banks = slices.Delete(sp.Spec.Banks, i, i+1)
		return nil
	}, ApplyOptions{Prune: true, SettleNow: settleNow})
	if err != nil {
		return setup.Resource{}, err
	}
	return a.Resource(setup.KindBank, id)
}

func (a *App) purgeBank(ctx context.Context, r setup.Resource) (*setup.Condition, error) {
	id := r.ID
	sends, cond, err := a.recordedSends(id)
	if cond != nil || err != nil {
		return cond, err
	}
	waiting, err := a.inflightWith(ctx, id, sends)
	if err != nil {
		return nil, err
	}
	if waiting != "" {
		return a.blocked(setup.ReasonSettlementPending, "%s", waiting), nil
	}
	b := a.bankByID(id)
	if b != nil {
		if err := a.retireBank(ctx, b); err != nil {
			return nil, fmt.Errorf("purge %s: %w", id, err)
		}
	}
	var errs []error
	if r.Allocated.Database != "" {
		errs = append(errs, a.dropBankDatabase(ctx, r.Allocated.Database))
	}
	errs = append(errs, os.RemoveAll(a.cfg.Path("banks", id)), a.memberGone(ctx, id))
	a.dropBankRows(id)
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("purge %s: %w", id, err)
	}
	tomb, err := a.readBackPurge(ctx, r)
	if err != nil {
		return nil, err
	}
	if err := a.ctl.Update(func(st *setup.Status) (bool, error) {
		st.Applied.Spec.Banks = slices.DeleteFunc(st.Applied.Spec.Banks, func(x setup.Bank) bool { return x.ID == id })
		st.Resources = slices.DeleteFunc(st.Resources, func(x setup.Resource) bool { return x.Kind == setup.KindBank && x.ID == id })
		keepTombstone(st, tomb)
		return true, resum(st)
	}); err != nil {
		return nil, err
	}
	a.clearSettleNow(setup.KindBank, id)
	a.lifeMu.Lock()
	a.mu.Lock()
	a.cfg.Banks = slices.DeleteFunc(slices.Clone(a.cfg.Banks), func(x *config.Bank) bool { return x.ID == id })
	a.mu.Unlock()
	delete(a.status, id)
	delete(a.confirms, id)
	a.lifeMu.Unlock()
	a.poke()
	return nil, nil
}

func (a *App) dropBankRows(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for cid, c := range a.st.Connections {
		if c.Institution == id {
			a.vaultDrop(cid)
		}
	}
	for lid, l := range a.st.LinkSessions {
		if l.Institution == id {
			delete(a.st.LinkSessions, lid)
		}
	}
	a.st.Revokes = slices.DeleteFunc(a.st.Revokes, func(p PendingRevoke) bool { return p.Bank == id })
	a.st.Faults = slices.DeleteFunc(a.st.Faults, func(f *server.Fault) bool { return f.Bank == id })
	a.dirty()
}

func (a *App) readBackPurge(ctx context.Context, r setup.Resource) (nip.Institution, error) {
	id := r.ID
	if r.Allocated.Database != "" {
		left, err := store.List(ctx, a.cfg.DatabaseURL, r.Allocated.Database)
		if err != nil {
			return nip.Institution{}, fmt.Errorf("read back the databases of %s: %w", id, err)
		}
		if slices.Contains(left, r.Allocated.Database) {
			return nip.Institution{}, fmt.Errorf("database %s of %s still exists after the purge", r.Allocated.Database, id)
		}
	}
	if _, err := os.Stat(a.cfg.Path("banks", id)); !os.IsNotExist(err) {
		return nip.Institution{}, fmt.Errorf("banks/%s still exists after the purge", id)
	}
	in, err := a.nibssCtl.member(ctx, id)
	if err != nil {
		return nip.Institution{}, fmt.Errorf("read back the NIBSS status of %s: %w", id, err)
	}
	if in.Status != nip.StatusRemoved {
		return nip.Institution{}, fmt.Errorf("NIBSS shows %s as %s, not %s", id, in.Status, nip.StatusRemoved)
	}
	return in, nil
}

func (a *App) purgePartner(_ context.Context, r setup.Resource) (*setup.Condition, error) {
	id := r.ID
	a.partnerMu.Lock()
	defer a.partnerMu.Unlock()
	a.dropPartnerRows(id)
	if err := os.RemoveAll(a.cfg.Path("partners", id)); err != nil {
		return nil, fmt.Errorf("remove partners/%s: %w", id, err)
	}
	if err := a.ctl.Update(func(st *setup.Status) (bool, error) {
		st.Applied.Spec.Partners = slices.DeleteFunc(st.Applied.Spec.Partners, func(x setup.Partner) bool { return x.ID == id })
		st.Resources = slices.DeleteFunc(st.Resources, func(x setup.Resource) bool { return x.Kind == setup.KindPartner && x.ID == id })
		return true, resum(st)
	}); err != nil {
		return nil, err
	}
	a.replacePartner(id, nil)
	return nil, nil
}

func (a *App) dropPartnerRows(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for cid, c := range a.st.Connections {
		if c.PartnerID == id {
			a.vaultDrop(cid)
			delete(a.st.Connections, cid)
		}
	}
	for aid, acct := range a.st.Accounts {
		if acct.PartnerID == id {
			delete(a.st.Accounts, aid)
			delete(a.st.Feeds, aid)
		}
	}
	for rid, ref := range a.st.Refreshes {
		if a.st.Accounts[ref.AccountID] == nil {
			delete(a.st.Refreshes, rid)
		}
	}
	for lid, l := range a.st.LinkSessions {
		if l.PartnerID == id {
			delete(a.st.LinkSessions, lid)
		}
	}
	a.st.Deliveries = slices.DeleteFunc(a.st.Deliveries, func(d *Delivery) bool { return d.PartnerID == id })
	a.dirty()
}

func (a *App) ctlPurgeImpact(w http.ResponseWriter, r *http.Request) {
	imp, err := a.PurgeImpact(r.Context(), r.PathValue("id"))
	if err != nil {
		a.bankError(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, imp)
}

func (a *App) ctlPurgeBank(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SettleNow bool `json:"settle_now"`
	}
	if err := web.DecodeBody(r, &req); err != nil && !errors.Is(err, io.EOF) {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", `Send nothing or {"settle_now":true}.`)
		return
	}
	res, err := a.PurgeBank(r.Context(), r.PathValue("id"), req.SettleNow)
	if err != nil {
		a.bankError(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusAccepted, res)
}

func (a *App) dropBankDatabase(ctx context.Context, name string) error {
	all, err := store.List(ctx, a.cfg.DatabaseURL, name)
	if err != nil {
		return fmt.Errorf("list the databases of %s: %w", name, err)
	}
	owned := slices.DeleteFunc(all, func(n string) bool { return n != name && !strings.HasPrefix(n, name+snapshotInfix) })
	if err := store.DropDatabases(ctx, a.cfg.DatabaseURL, owned); err != nil {
		return fmt.Errorf("drop database %s: %w", name, err)
	}
	return a.ctl.Release(setup.Owned{Kind: state.OwnedDatabase, Name: name})
}

func (a *App) bankRetired(id string) bool {
	st, err := a.ctl.Status()
	if err != nil {
		a.log.Error("read control.json", "err", err)
		return false
	}
	r := setup.FindResource(&st, setup.KindBank, id)
	return r != nil && r.Steps[stepRetired] == state.StepDone
}

func (a *App) retireBank(ctx context.Context, b *config.Bank) error {
	a.recordBank(b.ID, func(r *setup.Resource) bool {
		if r.Steps[stepRetired] == state.StepDone {
			return false
		}
		if r.Steps == nil {
			r.Steps = map[string]string{}
		}
		r.Steps[stepRetired] = state.StepDone
		return true
	})
	if !a.bankRetired(b.ID) {
		return fmt.Errorf("retire %s: the retirement was not saved", b.ID)
	}
	err := a.closeAtBank(ctx, b)
	if err != nil && server.Unreachable(err) && !a.mustRetireLive(b.ID) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("retire %s: %w", b.ID, err)
	}
	return nil
}

func (a *App) mustRetireLive(id string) bool {
	switch a.bankPhase(id) {
	case PhaseActive, PhaseSuspended, PhaseClosing, PhaseTerminating:
		return true
	}
	return false
}

func (a *App) closeAtBank(ctx context.Context, b *config.Bank) error {
	w, err := a.wishFor(b.ID)
	if err != nil {
		return err
	}
	if err := a.syncBank(ctx, b.ID, b.URL(), wishGeneration(a.WorldID(), w), w); err != nil {
		return err
	}
	admin, err := a.adminOf(b.ID)
	if err != nil {
		return err
	}
	state, err := admin.state(ctx)
	if err != nil {
		return err
	}
	if state.Phase != PhaseClosed {
		return fmt.Errorf("%s has not closed yet", b.ID)
	}
	return nil
}

func keepTombstone(st *setup.Status, in nip.Institution) {
	st.Tombstones = slices.DeleteFunc(st.Tombstones, func(x nip.Institution) bool { return x.ID == in.ID })
	st.Tombstones = append(st.Tombstones, in)
}
