package connect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

type PurgeImpact struct {
	Bank        BankStatus `json:"bank"`
	Database    string     `json:"database"`
	Snapshots   []string   `json:"snapshots"`
	Connections int        `json:"connections"`
	Blockers    []string   `json:"blockers"`
}

func (a *App) PurgeImpact(id string) (PurgeImpact, error) {
	st, ok := a.bankStatus(id)
	banks := a.bankList()
	b := findBank(banks, id)
	if !ok || b == nil {
		return PurgeImpact{}, errNotFound
	}
	snaps, err := a.snapshotsWith(id)
	if err != nil {
		return PurgeImpact{}, err
	}
	out := PurgeImpact{Bank: st, Database: b.Database, Snapshots: snaps, Blockers: []string{}}
	for _, other := range banks {
		if other.SettlesThrough == id {
			out.Blockers = append(out.Blockers, other.Name+" settles through this bank; purge it first")
		}
	}
	waiting, err := a.inflightWith(b.NIPCode, id)
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

func (a *App) snapshotsWith(id string) ([]string, error) {
	metas, err := a.snapshotMetas()
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, m := range metas {
		if slices.Contains(m.Banks, id) {
			out = append(out, m.ID)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (a *App) PurgeBank(id string, settleNow bool) (setup.Resource, error) {
	err := a.applyEdit(func(sp *setup.Spec) error {
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

func (a *App) purgeBank(r setup.Resource) (*setup.Condition, error) {
	id := r.ID
	waiting, err := a.inflightWith(r.Allocated.NIPCode, id)
	if err != nil {
		return nil, err
	}
	if waiting != "" {
		return a.blocked(setup.ReasonSettlementPending, "%s", waiting), nil
	}
	b := a.bankByID(id)
	snaps, err := a.snapshotsWith(id)
	if err != nil {
		return nil, err
	}
	a.sendLocks.Delete(id)
	var ls *ledgerSource
	if b != nil {
		a.detach(b, setup.StepSite)
		a.mu.Lock()
		ls = a.ledgerFor(id)
		a.mu.Unlock()
		a.detach(b, setup.StepDatabase)
	}
	var errs []error
	switch {
	case ls != nil:
		errs = append(errs, ls.Drop())
	case r.Allocated.Database != "":
		errs = append(errs, store.DropDatabases(context.Background(), a.cfg.DatabaseURL, []string{r.Allocated.Database}))
	}
	for _, snap := range snaps {
		errs = append(errs, a.dropSnapshotEverywhere(snap))
	}
	a.obMu.Lock()
	delete(a.obs, id)
	a.obMu.Unlock()
	errs = append(errs, os.RemoveAll(a.cfg.Path("banks", id)), a.removeMember(id))
	a.dropBankRows(id)
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("purge %s: %w", id, err)
	}
	if err := a.retireMember(r); err != nil {
		return nil, err
	}
	tomb, err := a.readBackPurge(r)
	if err != nil {
		return nil, err
	}
	if err := a.ctl.update(func(st *setup.Status) (bool, error) {
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
	a.dirty()
}

func (a *App) retireMember(r setup.Resource) error {
	sw := a.nibssStore()
	if sw == nil {
		return errors.New("NIBSS is not open, so the id of the removed bank cannot be retired; try again soon")
	}
	code := r.Allocated.NIPCode
	if err := sw.Retire(context.Background(), code, r.ID+"-"+code); err != nil {
		return fmt.Errorf("retire the NIBSS id of %s: %w", r.ID, err)
	}
	return nil
}

func (a *App) readBackPurge(r setup.Resource) (nibss.Institution, error) {
	id := r.ID
	if r.Allocated.Database != "" {
		left, err := store.List(context.Background(), a.cfg.DatabaseURL, r.Allocated.Database)
		if err != nil {
			return nibss.Institution{}, fmt.Errorf("read back the databases of %s: %w", id, err)
		}
		if slices.Contains(left, r.Allocated.Database) {
			return nibss.Institution{}, fmt.Errorf("database %s of %s still exists after the purge", r.Allocated.Database, id)
		}
	}
	if _, err := os.Stat(a.cfg.Path("banks", id)); !os.IsNotExist(err) {
		return nibss.Institution{}, fmt.Errorf("banks/%s still exists after the purge", id)
	}
	sw := a.nibssStore()
	if sw == nil {
		return nibss.Institution{}, errors.New("NIBSS is not open, so its tombstone for the bank cannot be read back; try again soon")
	}
	in, err := sw.Institution(context.Background(), r.Allocated.NIPCode)
	if err != nil {
		return nibss.Institution{}, fmt.Errorf("read back the NIBSS status of %s: %w", id, err)
	}
	if in.Status != nibss.StatusRemoved {
		return nibss.Institution{}, fmt.Errorf("NIBSS shows %s as %s, not %s", id, in.Status, nibss.StatusRemoved)
	}
	return in, nil
}

func (a *App) dropSnapshotEverywhere(snap string) error {
	a.mu.Lock()
	others := slices.Clone(a.ledgers)
	r := a.rtgs
	a.mu.Unlock()
	var errs []error
	for _, l := range others {
		errs = append(errs, l.DropSnapshot(snap))
	}
	if r != nil {
		errs = append(errs, r.DropSnapshot(snap))
	}
	errs = append(errs, a.dropNIBSSSnapshot(snap))
	if err := os.Remove(filepath.Join(a.snapshotDir(), snap+".json")); err != nil && !os.IsNotExist(err) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (a *App) purgePartner(r setup.Resource) (*setup.Condition, error) {
	id := r.ID
	a.partnerMu.Lock()
	defer a.partnerMu.Unlock()
	a.dropPartnerRows(id)
	if err := os.RemoveAll(a.cfg.Path("partners", id)); err != nil {
		return nil, fmt.Errorf("remove partners/%s: %w", id, err)
	}
	if err := a.ctl.update(func(st *setup.Status) (bool, error) {
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
	imp, err := a.PurgeImpact(r.PathValue("id"))
	if err != nil {
		a.bankError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, imp)
}

func (a *App) ctlPurgeBank(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SettleNow bool `json:"settle_now"`
	}
	if err := decodeBody(r, &req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", `Send nothing or {"settle_now":true}.`)
		return
	}
	res, err := a.PurgeBank(r.PathValue("id"), req.SettleNow)
	if err != nil {
		a.bankError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, res)
}
