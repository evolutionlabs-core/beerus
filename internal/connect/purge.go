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

type PurgeResult struct {
	Bank      string   `json:"bank"`
	Database  string   `json:"database"`
	Snapshots []string `json:"snapshots_deleted"`
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
	if st.Phase != PhaseClosed {
		out.Blockers = append(out.Blockers, "the bank is "+st.Phase+"; close it before purging it")
	}
	if len(banks) == 1 {
		out.Blockers = append(out.Blockers, "it is the only bank left; add another bank first")
	}
	for _, other := range banks {
		if other.SettlesThrough == id {
			out.Blockers = append(out.Blockers, other.Name+" settles through this bank; purge it first")
		}
	}
	sw := a.nibssStore()
	if sw == nil {
		return PurgeImpact{}, errors.New("NIBSS is not available right now")
	}
	sessions, err := sw.Open(context.Background())
	if err != nil {
		return PurgeImpact{}, fmt.Errorf("check open NIP sessions: %w", err)
	}
	waiting := 0
	for _, x := range sessions {
		if x.FromCode == b.NIPCode || x.ToCode == b.NIPCode {
			waiting++
		}
	}
	switch {
	case waiting == 1:
		out.Blockers = append(out.Blockers, "1 NIP session with this bank has not settled; wait for the next settlement or run beerus nip settle-now")
	case waiting > 1:
		out.Blockers = append(out.Blockers, fmt.Sprintf("%d NIP sessions with this bank have not settled; wait for the next settlement or run beerus nip settle-now", waiting))
	}
	unposted, err := sw.Unposted(context.Background())
	if err != nil {
		return PurgeImpact{}, fmt.Errorf("check unposted settlements: %w", err)
	}
	awaiting := 0
	for _, settlement := range unposted {
		if slices.ContainsFunc(settlement.Sessions, func(x nibss.Session) bool { return x.FromCode == b.NIPCode || x.ToCode == b.NIPCode }) {
			awaiting++
		}
	}
	switch {
	case awaiting == 1:
		out.Blockers = append(out.Blockers, "1 settlement with this bank has not been posted to the books; wait for the next step and try again")
	case awaiting > 1:
		out.Blockers = append(out.Blockers, fmt.Sprintf("%d settlements with this bank have not been posted to the books; wait for the next step and try again", awaiting))
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

func (a *App) markPurged(id string) (PurgeImpact, *config.Bank, error) {
	a.recMu.Lock()
	defer a.recMu.Unlock()
	imp, err := a.PurgeImpact(id)
	if err != nil {
		return PurgeImpact{}, nil, err
	}
	if len(imp.Blockers) > 0 {
		return PurgeImpact{}, nil, invalid("%s cannot be purged: %s", imp.Bank.Name, strings.Join(imp.Blockers, "; "))
	}
	a.lifeMu.Lock()
	b := a.bankByID(id)
	st := a.status[id]
	a.mu.Lock()
	banks := a.cfg.Banks
	a.cfg.Banks = slices.DeleteFunc(slices.Clone(banks), func(x *config.Bank) bool { return x.ID == id })
	a.mu.Unlock()
	delete(a.status, id)
	a.lifeMu.Unlock()
	if err := a.saveBanks(func() {
		a.mu.Lock()
		a.cfg.Banks = banks
		a.mu.Unlock()
		a.status[id] = st
	}); err != nil {
		return PurgeImpact{}, nil, err
	}
	return imp, b, nil
}

func (a *App) PurgeBank(id string) (PurgeResult, error) {
	a.gate.RLock()
	defer a.gate.RUnlock()
	imp, b, err := a.markPurged(id)
	if err != nil {
		return PurgeResult{}, err
	}
	a.sendLocks.Delete(id)
	memberErr := a.removeMember(id)
	a.detach(b, setup.StepSite)
	ls := a.ledgerFor(id)
	a.detach(b, setup.StepDatabase)
	errs := []error{memberErr}
	if ls != nil {
		errs = append(errs, ls.Drop())
	} else if err := store.DropDatabases(context.Background(), a.cfg.DatabaseURL, []string{b.Database}); err != nil {
		errs = append(errs, err)
	} else {
		errs = append(errs, a.ctl.forget(setup.KindBank, id))
	}
	for _, snap := range imp.Snapshots {
		a.mu.Lock()
		others := slices.Clone(a.ledgers)
		a.mu.Unlock()
		for _, l := range others {
			errs = append(errs, l.DropSnapshot(snap))
		}
		if a.rtgs != nil {
			errs = append(errs, a.rtgs.DropSnapshot(snap))
		}
		errs = append(errs, a.dropNIBSSSnapshot(snap))
		if err := os.Remove(filepath.Join(a.snapshotDir(), snap+".json")); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	a.poke()
	res := PurgeResult{Bank: id, Database: imp.Database, Snapshots: imp.Snapshots}
	if err := errors.Join(errs...); err != nil {
		a.lifeMu.Lock()
		a.warn("%s was purged, but something was left behind (%v); run beerus doctor --fix", id, err)
		a.lifeMu.Unlock()
		return res, err
	}
	return res, nil
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
	writeJSON(w, http.StatusOK, res)
}
