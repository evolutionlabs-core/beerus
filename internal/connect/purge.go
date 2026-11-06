package connect

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/config"
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
	b := a.cfg.Bank(id)
	if !ok || b == nil {
		return PurgeImpact{}, errNotFound
	}
	out := PurgeImpact{Bank: st, Database: b.Database, Snapshots: a.snapshotsWith(id), Blockers: []string{}}
	if st.Phase != PhaseClosed {
		out.Blockers = append(out.Blockers, "the bank is "+st.Phase+"; close it before purging it")
	}
	if len(a.cfg.Banks) == 1 {
		out.Blockers = append(out.Blockers, "it is the only bank left; add another bank first")
	}
	for _, other := range a.cfg.Banks {
		if other.SettlesThrough == id {
			out.Blockers = append(out.Blockers, other.Name+" settles through this bank; purge it first")
		}
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

func (a *App) snapshotsWith(id string) []string {
	out := []string{}
	entries, _ := os.ReadDir(a.snapshotDir())
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(a.snapshotDir(), e.Name()))
		if err != nil {
			continue
		}
		var f struct {
			Meta snapshotMeta `json:"meta"`
		}
		if json.Unmarshal(raw, &f) == nil && slices.Contains(f.Meta.Banks, id) {
			out = append(out, f.Meta.ID)
		}
	}
	sort.Strings(out)
	return out
}

func (a *App) PurgeBank(id string) (PurgeResult, error) {
	a.recMu.Lock()
	defer a.recMu.Unlock()
	imp, err := a.PurgeImpact(id)
	if err != nil {
		return PurgeResult{}, err
	}
	if len(imp.Blockers) > 0 {
		return PurgeResult{}, invalid("%s cannot be purged: %s", imp.Bank.Name, strings.Join(imp.Blockers, "; "))
	}
	a.mu.Lock()
	if err := a.world.Purge(id); err != nil {
		a.mu.Unlock()
		return PurgeResult{}, invalid("%s", err.Error())
	}
	err = a.saveLocked()
	a.mu.Unlock()
	if err != nil {
		return PurgeResult{}, err
	}
	a.lifeMu.Lock()
	b := a.cfg.Bank(id)
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
		return PurgeResult{}, err
	}
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
