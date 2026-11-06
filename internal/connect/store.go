package connect

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"sync"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

const (
	ownedDatabase = "database"
	ownedSnapshot = "snapshot"
	ownedPort     = "port"
	ownedDir      = "dir"
)

var errStoreReadOnly = errors.New("only the simulator process writes control.json")

type ctlStore struct {
	mu       sync.Mutex
	dir      string
	st       setup.Status
	version  uint64
	readOnly bool

	writeMu sync.Mutex
	written uint64
}

func openStore(dir string) (*ctlStore, error) {
	st, err := setup.LoadStatus(dir)
	if err != nil {
		return nil, err
	}
	return &ctlStore{dir: dir, st: st}, nil
}

func readStore(dir string) (*ctlStore, error) {
	s, err := openStore(dir)
	if err != nil {
		return nil, err
	}
	s.readOnly = true
	return s, nil
}

func createStore(dir string, st setup.Status) (*ctlStore, error) {
	s := &ctlStore{dir: dir, st: st}
	if err := s.update(func(*setup.Status) (bool, error) { return true, nil }); err != nil {
		return nil, err
	}
	return s, nil
}

func newSetupKey() (uint64, error) {
	var b [8]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			return 0, fmt.Errorf("make setup key: %w", err)
		}
		if k := binary.BigEndian.Uint64(b[:]); k != 0 {
			return k, nil
		}
	}
}

func cloneStatus(st setup.Status) (setup.Status, error) {
	raw, err := json.Marshal(st)
	if err != nil {
		return setup.Status{}, fmt.Errorf("copy status: %w", err)
	}
	var out setup.Status
	if err := json.Unmarshal(raw, &out); err != nil {
		return setup.Status{}, fmt.Errorf("copy status: %w", err)
	}
	return out, nil
}

func (s *ctlStore) status() (setup.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneStatus(s.st)
}

func (s *ctlStore) setupKey() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.SetupKey
}

func (s *ctlStore) change(fn func(*setup.Status) (bool, error)) (uint64, error) {
	if s.readOnly {
		return 0, errStoreReadOnly
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	work, err := cloneStatus(s.st)
	if err != nil {
		return 0, err
	}
	changed, err := fn(&work)
	if err != nil || !changed {
		return 0, err
	}
	s.st = work
	s.version++
	return s.version, nil
}

func (s *ctlStore) flush(version uint64) error {
	if version == 0 {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if version <= s.written {
		return nil
	}
	s.mu.Lock()
	st, err := cloneStatus(s.st)
	latest := s.version
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if err := setup.SaveStatus(s.dir, st); err != nil {
		return err
	}
	s.written = latest
	return nil
}

func (s *ctlStore) update(fn func(*setup.Status) (bool, error)) error {
	v, err := s.change(fn)
	if err != nil {
		return err
	}
	return s.flush(v)
}

func findResource(st *setup.Status, kind, id string) *setup.Resource {
	for i := range st.Resources {
		if st.Resources[i].Kind == kind && st.Resources[i].ID == id {
			return &st.Resources[i]
		}
	}
	return nil
}

func ensureResource(st *setup.Status, kind, id string) *setup.Resource {
	if r := findResource(st, kind, id); r != nil {
		return r
	}
	st.Resources = append(st.Resources, setup.Resource{Kind: kind, ID: id})
	return &st.Resources[len(st.Resources)-1]
}

func (s *ctlStore) Claim(kind, id string, list ...setup.Owned) error {
	return s.update(func(st *setup.Status) (bool, error) {
		r := ensureResource(st, kind, id)
		changed := false
		for _, o := range list {
			if !slices.Contains(r.Owned, o) {
				r.Owned = append(r.Owned, o)
				changed = true
			}
		}
		return changed, nil
	})
}

func (s *ctlStore) Release(o setup.Owned) error {
	return s.update(func(st *setup.Status) (bool, error) {
		changed := false
		for i := range st.Resources {
			r := &st.Resources[i]
			if n := len(r.Owned); n > 0 {
				r.Owned = slices.DeleteFunc(r.Owned, func(x setup.Owned) bool { return x == o })
				changed = changed || len(r.Owned) != n
			}
		}
		return changed, nil
	})
}

func (s *ctlStore) forget(kind, id string) error {
	return s.update(func(st *setup.Status) (bool, error) {
		n := len(st.Resources)
		st.Resources = slices.DeleteFunc(st.Resources, func(r setup.Resource) bool { return r.Kind == kind && r.ID == id })
		return len(st.Resources) != n, nil
	})
}

func owned(st setup.Status, kinds ...string) map[string]string {
	out := map[string]string{}
	for _, r := range st.Resources {
		for _, o := range r.Owned {
			if slices.Contains(kinds, o.Kind) {
				out[o.Name] = r.Kind + "/" + r.ID
			}
		}
	}
	return out
}

func bankOwned(b *config.Bank) []setup.Owned {
	list := []setup.Owned{{Kind: ownedDatabase, Name: b.Database}, {Kind: ownedPort, Name: strconv.Itoa(b.Port)}}
	if b.APIPort != 0 {
		list = append(list, setup.Owned{Kind: ownedPort, Name: strconv.Itoa(b.APIPort)})
	}
	return list
}

func (a *App) claimBank(b *config.Bank) error {
	return a.ctl.Claim(setup.KindBank, b.ID, bankOwned(b)...)
}

func specBank(b config.Bank, prev setup.Bank) setup.Bank {
	return setup.Bank{
		ID:             b.ID,
		Name:           b.Name,
		Type:           b.Type,
		NIPCode:        b.NIPCode,
		NUBANCode:      b.NUBANCode,
		SettlesThrough: b.SettlesThrough,
		State:          b.State,
		NetDebitCap:    b.NetDebitCap,
		Collateral:     b.Collateral,
		OpenBanking:    setup.OpenBanking{ConsentMonths: b.API.ConsentMonths, RefreshDays: b.API.RefreshDays, DormantDays: b.API.DormantDays, AssertionSeconds: b.API.AssertionSeconds},
		OnRemove:       prev.OnRemove,
	}
}

func syncBanks(st *setup.Status, banks []config.Bank) (bool, error) {
	prev := map[string]setup.Bank{}
	for _, b := range st.Applied.Spec.Banks {
		prev[b.ID] = b
	}
	changed := false
	next := make([]setup.Bank, 0, len(banks))
	keep := map[string]bool{}
	for _, b := range banks {
		keep[b.ID] = true
		sb := specBank(b, prev[b.ID])
		alloc := setup.Allocation{NIPCode: b.NIPCode, NUBANCode: b.NUBANCode, Database: b.Database, Port: b.Port, APIPort: b.APIPort}
		r := findResource(st, setup.KindBank, b.ID)
		old, existed := prev[b.ID]
		switch {
		case r == nil || !existed:
			r = ensureResource(st, setup.KindBank, b.ID)
			r.Generation++
			r.Phase = setup.PhaseProvisioning
			r.Steps = map[string]string{setup.StepAllocate: stepDone}
			changed = true
		case !reflect.DeepEqual(old, sb):
			r.Generation++
			changed = true
		}
		if r.Allocated != alloc {
			r.Allocated = alloc
			changed = true
		}
		next = append(next, sb)
	}
	for id := range prev {
		if !keep[id] {
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	st.Applied.Spec.Banks = next
	sum, err := specSum(st.Applied)
	if err != nil {
		return false, err
	}
	st.AppliedSHA256 = sum
	return true, nil
}

func (a *App) recordBanks() (uint64, error) {
	a.mu.Lock()
	banks := make([]config.Bank, 0, len(a.cfg.Banks))
	for _, b := range a.cfg.Banks {
		banks = append(banks, *b)
	}
	a.mu.Unlock()
	return a.ctl.change(func(st *setup.Status) (bool, error) { return syncBanks(st, banks) })
}

func (a *App) saveBanks(undo func()) error {
	v, err := a.recordBanks()
	if err == nil {
		if err = a.ctl.flush(v); err == nil {
			return nil
		}
	}
	a.lifeMu.Lock()
	undo()
	a.lifeMu.Unlock()
	if v, rerr := a.recordBanks(); rerr == nil {
		_ = a.ctl.flush(v)
	}
	return err
}
