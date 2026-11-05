package connect

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

type DoctorReport struct {
	Databases     []string `json:"databases"`
	Orphans       []string `json:"orphans"`
	Missing       []string `json:"missing"`
	DeadSnapshots []string `json:"dead_snapshots"`
	StaleOwned    []string `json:"stale_owned"`
	Fixed         bool     `json:"fixed"`
}

func (r DoctorReport) Clean() bool {
	return len(r.Orphans)+len(r.DeadSnapshots)+len(r.StaleOwned) == 0
}

func Doctor(ctx context.Context, c *config.Config, fix bool) (DoctorReport, error) {
	rep := DoctorReport{Databases: []string{}, Orphans: []string{}, Missing: []string{}, DeadSnapshots: []string{}, StaleOwned: []string{}}
	if fix {
		if _, err := os.Stat(c.Path("running")); err == nil {
			return rep, errors.New("beerus up is running; stop it before using --fix (or delete " + c.Path("running") + " if it is not)")
		}
	}
	st, err := setup.LoadStatus(c.Dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return rep, err
	}
	expected := map[string]bool{}
	for name := range owned(st, ownedDatabase, ownedSnapshot) {
		expected[name] = true
	}
	cbn := config.DatabaseName(c.Dir, cbnID)
	if len(c.Banks) > 0 {
		expected[cbn] = true
	}
	byID := map[string]*config.Bank{}
	for _, b := range c.Banks {
		expected[b.Database] = true
		byID[b.ID] = b
	}
	dir := c.Path("snapshots")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var f struct {
			Meta snapshotMeta `json:"meta"`
		}
		if json.Unmarshal(raw, &f) != nil {
			continue
		}
		copies := []string{}
		for _, id := range f.Meta.Banks {
			if b := byID[id]; b != nil {
				copies = append(copies, b.Database+"_snap_"+snapID(f.Meta.ID))
			}
		}
		if len(copies) < len(f.Meta.Banks) {
			rep.DeadSnapshots = append(rep.DeadSnapshots, f.Meta.ID)
			continue
		}
		for _, n := range copies {
			expected[n] = true
		}
		if f.Meta.CBN {
			expected[cbn+"_snap_"+snapID(f.Meta.ID)] = true
		}
	}
	prefix := config.DatabaseName(c.Dir, "")
	all, err := store.List(ctx, c.DatabaseURL, "beerus_")
	if err != nil {
		return rep, err
	}
	found := map[string]bool{}
	for _, n := range all {
		if !strings.HasPrefix(n, prefix) && !expected[n] {
			continue
		}
		found[n] = true
		rep.Databases = append(rep.Databases, n)
		if !expected[n] {
			rep.Orphans = append(rep.Orphans, n)
		}
	}
	for n := range expected {
		if !found[n] {
			rep.Missing = append(rep.Missing, n)
		}
	}
	sort.Strings(rep.Missing)
	for _, r := range st.Resources {
		if r.Kind == setup.KindBank && byID[r.ID] == nil {
			rep.StaleOwned = append(rep.StaleOwned, r.ID)
		}
	}
	if !fix || rep.Clean() {
		return rep, nil
	}
	if err := store.DropDatabases(ctx, c.DatabaseURL, rep.Orphans); err != nil {
		return rep, err
	}
	for _, id := range rep.DeadSnapshots {
		if err := os.Remove(filepath.Join(dir, id+".json")); err != nil && !os.IsNotExist(err) {
			return rep, err
		}
	}
	if len(rep.StaleOwned) > 0 {
		ctl, err := openStore(c.Dir)
		if err != nil {
			return rep, err
		}
		for _, bank := range rep.StaleOwned {
			if err := ctl.forget(setup.KindBank, bank); err != nil {
				return rep, err
			}
		}
	}
	rep.Fixed = true
	return rep, nil
}
