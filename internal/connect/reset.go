package connect

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

var (
	ErrRunning   = errors.New("Beerus is running; stop it first, or delete the running file if it is not")
	ErrWrongName = errors.New("the name does not match this setup")
)

type ResetTarget struct {
	Dir         string
	Name        string
	DatabaseURL string
}

type oldSetup struct {
	Name        string `yaml:"name"`
	DatabaseURL string `yaml:"database_url"`
	Metadata    struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
}

func readYAML(path string, v any) (bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	if err := yaml.Unmarshal(raw, v); err != nil {
		return false, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	return true, nil
}

func FindResetTarget(dir string) (ResetTarget, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ResetTarget{}, fmt.Errorf("resolve %s: %w", dir, err)
	}
	t := ResetTarget{Dir: dir}
	var old oldSetup
	hasOld, err := readYAML(filepath.Join(dir, oldSpecFile), &old)
	if err != nil {
		return t, err
	}
	var current oldSetup
	hasSpec := false
	_, statErr := os.Stat(filepath.Join(dir, statusFile))
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return t, fmt.Errorf("look for %s: %w", statusFile, statErr)
	}
	hasStatus := statErr == nil
	if hasStatus {
		if hasSpec, err = readYAML(filepath.Join(dir, SpecFile), &current); err != nil {
			return t, err
		}
	}
	var local setup.Local
	if _, err := readYAML(filepath.Join(dir, LocalFile), &local); err != nil {
		return t, err
	}
	switch {
	case hasSpec && current.Metadata.Name != "":
		t.Name = current.Metadata.Name
	case hasOld && old.Metadata.Name != "":
		t.Name = old.Metadata.Name
	case hasOld && old.Name != "":
		t.Name = old.Name
	case hasOld || hasStatus:
		t.Name = filepath.Base(abs)
	default:
		return t, fmt.Errorf("%s holds no Beerus setup", dir)
	}
	t.DatabaseURL = defaultDatabaseURL
	for _, u := range []string{os.Getenv(config.DatabaseURLEnv), local.DatabaseURL, old.DatabaseURL} {
		if u != "" {
			t.DatabaseURL = u
			break
		}
	}
	return t, nil
}

func (t ResetTarget) Check(typed string) error {
	if typed != t.Name {
		return fmt.Errorf("%w: %s holds setup %q, not %q", ErrWrongName, t.Dir, t.Name, typed)
	}
	return nil
}

func databasePrefix(dir string) string {
	name := config.DatabaseName(dir, "x")
	return strings.TrimSuffix(name, "x")
}

func Reset(ctx context.Context, dir, databaseURL string) ([]string, error) {
	if _, err := os.Stat(filepath.Join(dir, "running")); err == nil {
		return nil, fmt.Errorf("reset %s: %w", dir, ErrRunning)
	}
	prefix := databasePrefix(dir)
	found, err := store.List(ctx, databaseURL, prefix)
	if err != nil {
		return nil, fmt.Errorf("list databases of %s: %w", dir, err)
	}
	var names []string
	for _, n := range found {
		if strings.HasPrefix(n, prefix) {
			names = append(names, n)
		}
	}
	st, err := setup.LoadStatus(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, setup.ErrOldSetup) && !setup.IsCorrupt(err) {
		return nil, err
	}
	for n := range owned(st, ownedDatabase) {
		if !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	if err := store.DropDatabases(ctx, databaseURL, names); err != nil {
		return nil, fmt.Errorf("drop databases of %s: %w", dir, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.Name() == LocalFile {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return nil, fmt.Errorf("remove %s: %w", e.Name(), err)
		}
	}
	return names, nil
}
