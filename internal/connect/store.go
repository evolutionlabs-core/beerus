package connect

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
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

func (s *ctlStore) saved() error {
	s.mu.Lock()
	v := s.version
	s.mu.Unlock()
	return s.flush(v)
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
