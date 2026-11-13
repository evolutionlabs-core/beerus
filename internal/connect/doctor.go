package connect

import (
	"context"
	"errors"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

type DoctorReport struct {
	Databases  []string `json:"databases"`
	Orphans    []string `json:"orphans"`
	Missing    []string `json:"missing"`
	StaleOwned []string `json:"stale_owned"`
	Fixed      bool     `json:"fixed"`
}

func (r DoctorReport) Clean() bool {
	return len(r.Orphans)+len(r.StaleOwned) == 0
}

func Doctor(ctx context.Context, c *config.Config, fix bool) (DoctorReport, error) {
	rep := DoctorReport{Databases: []string{}, Orphans: []string{}, Missing: []string{}, StaleOwned: []string{}}
	if fix {
		if _, err := os.Stat(c.Path("running")); err == nil {
			return rep, errors.New("beerus up is running; stop it before using --fix (or delete " + c.Path("running") + " if it is not)")
		}
	}
	st, err := setup.LoadStatus(c.Dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return rep, err
	}
	live := setup.Status{}
	for _, r := range st.Resources {
		if r.Kind != setup.KindBank || c.Bank(r.ID) != nil {
			live.Resources = append(live.Resources, r)
		}
	}
	expected := map[string]bool{}
	for name := range owned(live, ownedDatabase) {
		expected[name] = true
	}
	if len(c.Banks) > 0 {
		expected[config.DatabaseName(c.Dir, cbnID)] = true
	}
	byID := map[string]*config.Bank{}
	for _, b := range c.Banks {
		expected[b.Database] = true
		byID[b.ID] = b
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
			for _, o := range r.Owned {
				if (o.Kind == ownedDatabase) && !slices.Contains(rep.Orphans, o.Name) {
					rep.Orphans = append(rep.Orphans, o.Name)
				}
			}
		}
	}
	if !fix || rep.Clean() {
		return rep, nil
	}
	var ctl *ctlStore
	if len(rep.StaleOwned) > 0 {
		var err error
		ctl, err = openStore(c.Dir)
		if err != nil {
			return rep, err
		}
	}
	if err := store.DropDatabases(ctx, c.DatabaseURL, rep.Orphans); err != nil {
		return rep, err
	}
	for _, bank := range rep.StaleOwned {
		if err := ctl.forget(setup.KindBank, bank); err != nil {
			return rep, err
		}
	}
	rep.Fixed = true
	return rep, nil
}
