package state

import (
	"encoding/json"
	"fmt"
	"slices"
	"sync"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

const (
	OwnedDatabase = "database"
	OwnedPort     = "port"
	OwnedDir      = "dir"
	StepDone      = "done"
)

type Store struct {
	mu      sync.Mutex
	dir     string
	st      setup.Status
	version uint64

	writeMu sync.Mutex
	written uint64
}

func Open(dir string) (*Store, error) {
	st, err := setup.LoadStatus(dir)
	if err != nil {
		return nil, err
	}
	return &Store{dir: dir, st: st}, nil
}

func Create(dir string, st setup.Status) (*Store, error) {
	s := &Store{dir: dir, st: st}
	if err := s.Update(func(*setup.Status) (bool, error) { return true, nil }); err != nil {
		return nil, err
	}
	return s, nil
}

func CloneStatus(st setup.Status) (setup.Status, error) {
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

func (s *Store) Status() (setup.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return CloneStatus(s.st)
}

func (s *Store) SetupKey() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.SetupKey
}

func (s *Store) change(fn func(*setup.Status) (bool, error)) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	work, err := CloneStatus(s.st)
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

func (s *Store) flush(version uint64) error {
	if version == 0 {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if version <= s.written {
		return nil
	}
	s.mu.Lock()
	st, err := CloneStatus(s.st)
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

func (s *Store) DurableStatus() (setup.Status, error) {
	s.mu.Lock()
	v := s.version
	s.mu.Unlock()
	if err := s.flush(v); err != nil {
		return setup.Status{}, err
	}
	return s.Status()
}

func (s *Store) Update(fn func(*setup.Status) (bool, error)) error {
	v, err := s.change(fn)
	if err != nil {
		return err
	}
	return s.flush(v)
}

func FindResource(st *setup.Status, kind, id string) *setup.Resource {
	for i := range st.Resources {
		if st.Resources[i].Kind == kind && st.Resources[i].ID == id {
			return &st.Resources[i]
		}
	}
	return nil
}

func EnsureResource(st *setup.Status, kind, id string) *setup.Resource {
	if r := FindResource(st, kind, id); r != nil {
		return r
	}
	st.Resources = append(st.Resources, setup.Resource{Kind: kind, ID: id})
	return &st.Resources[len(st.Resources)-1]
}

func MarkActive(s *Store, system string) error {
	return s.Update(func(st *setup.Status) (bool, error) {
		r := EnsureResource(st, setup.KindSystem, system)
		if r.Phase == config.PhaseActive {
			return false, nil
		}
		r.Phase = config.PhaseActive
		return true, nil
	})
}

func (s *Store) Claim(kind, id string, list ...setup.Owned) error {
	return s.Update(func(st *setup.Status) (bool, error) {
		r := EnsureResource(st, kind, id)
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

func (s *Store) Release(o setup.Owned) error {
	return s.Update(func(st *setup.Status) (bool, error) {
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

func (s *Store) ReleasePorts(kind, id string) error {
	st, err := s.Status()
	if err != nil {
		return err
	}
	r := FindResource(&st, kind, id)
	if r == nil {
		return nil
	}
	for _, o := range slices.Clone(r.Owned) {
		if o.Kind != OwnedPort {
			continue
		}
		if err := s.Release(o); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Forget(kind, id string) error {
	return s.Update(func(st *setup.Status) (bool, error) {
		n := len(st.Resources)
		st.Resources = slices.DeleteFunc(st.Resources, func(r setup.Resource) bool { return r.Kind == kind && r.ID == id })
		return len(st.Resources) != n, nil
	})
}

func Owned(st setup.Status, kinds ...string) map[string]string {
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
