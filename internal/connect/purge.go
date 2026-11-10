package connect

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

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
	if len(banks) == 1 {
		out.Blockers = append(out.Blockers, "it is the only bank left; add another bank first")
	}
	for _, other := range banks {
		if other.SettlesThrough == id {
			out.Blockers = append(out.Blockers, other.Name+" settles through this bank; purge it first")
		}
	}
	waiting, err := a.unsettledWith(b.NIPCode)
	if err != nil {
		return PurgeImpact{}, err
	}
	out.Blockers = append(out.Blockers, waiting...)
	a.mu.Lock()
	for _, c := range a.st.Connections {
		if c.Institution == id {
			out.Connections++
		}
	}
	a.mu.Unlock()
	return out, nil
}

func (a *App) unsettledWith(code string) ([]string, error) {
	sw := a.nibssStore()
	if sw == nil {
		return nil, errors.New("NIBSS is not available right now")
	}
	sessions, err := sw.Open(context.Background())
	if err != nil {
		return nil, fmt.Errorf("check open NIP sessions: %w", err)
	}
	waiting := 0
	for _, x := range sessions {
		if x.FromCode == code || x.ToCode == code {
			waiting++
		}
	}
	var out []string
	switch {
	case waiting == 1:
		out = append(out, "1 NIP session with this bank has not settled; wait for the next settlement or run beerus nip settle-now")
	case waiting > 1:
		out = append(out, fmt.Sprintf("%d NIP sessions with this bank have not settled; wait for the next settlement or run beerus nip settle-now", waiting))
	}
	unposted, err := sw.Unposted(context.Background())
	if err != nil {
		return nil, fmt.Errorf("check unposted settlements: %w", err)
	}
	awaiting := 0
	for _, settlement := range unposted {
		if slices.ContainsFunc(settlement.Sessions, func(x nibss.Session) bool { return x.FromCode == code || x.ToCode == code }) {
			awaiting++
		}
	}
	switch {
	case awaiting == 1:
		out = append(out, "1 settlement with this bank has not been posted to the books; wait for the next step and try again")
	case awaiting > 1:
		out = append(out, fmt.Sprintf("%d settlements with this bank have not been posted to the books; wait for the next step and try again", awaiting))
	}
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

func (a *App) PurgeBank(id string) (setup.Resource, error) {
	err := a.applyEdit(func(sp *setup.Spec) error {
		i := slices.IndexFunc(sp.Spec.Banks, func(b setup.Bank) bool { return b.ID == id })
		if i < 0 {
			return errNotFound
		}
		sp.Spec.Banks = slices.Delete(sp.Spec.Banks, i, i+1)
		return nil
	}, ApplyOptions{Prune: true})
	if err != nil {
		return setup.Resource{}, err
	}
	return a.Resource(setup.KindBank, id)
}

func (a *App) purgeBank(r setup.Resource) (*setup.Condition, error) {
	id := r.ID
	waiting, err := a.unsettledWith(r.Allocated.NIPCode)
	if err != nil {
		return nil, err
	}
	if len(waiting) > 0 {
		return a.blocked(setup.ReasonSettlementPending, "%s", strings.Join(waiting, "; ")), nil
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
	errs = append(errs, os.RemoveAll(a.cfg.Path("banks", id)), a.removeMember(id))
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("purge %s: %w", id, err)
	}
	if err := a.ctl.update(func(st *setup.Status) (bool, error) {
		st.Applied.Spec.Banks = slices.DeleteFunc(st.Applied.Spec.Banks, func(x setup.Bank) bool { return x.ID == id })
		st.Resources = slices.DeleteFunc(st.Resources, func(x setup.Resource) bool { return x.Kind == setup.KindBank && x.ID == id })
		return true, resum(st)
	}); err != nil {
		return nil, err
	}
	a.lifeMu.Lock()
	a.mu.Lock()
	a.cfg.Banks = slices.DeleteFunc(slices.Clone(a.cfg.Banks), func(x *config.Bank) bool { return x.ID == id })
	a.mu.Unlock()
	delete(a.status, id)
	a.lifeMu.Unlock()
	a.poke()
	return nil, nil
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

func (a *App) ctlPurgeImpact(w http.ResponseWriter, r *http.Request) {
	imp, err := a.PurgeImpact(r.PathValue("id"))
	if err != nil {
		a.bankError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, imp)
}

func (a *App) ctlPurgeBank(w http.ResponseWriter, r *http.Request) {
	res, err := a.PurgeBank(r.PathValue("id"))
	if err != nil {
		a.bankError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, res)
}
