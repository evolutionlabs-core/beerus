package connect

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

var (
	ErrRunning   = errors.New("Beerus is running; stop it first")
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
	var spec setup.Spec
	hasSpec := false
	if _, err := os.Stat(filepath.Join(dir, statusFile)); err == nil {
		raw, err := os.ReadFile(filepath.Join(dir, SpecFile))
		if err != nil {
			return t, fmt.Errorf("read %s: %w", SpecFile, err)
		}
		parsed, errs := setup.Parse(SpecFile, raw)
		if len(errs) > 0 {
			return t, errors.Join(errs...)
		}
		spec, hasSpec = parsed, true
	}
	var local setup.Local
	if _, err := readYAML(filepath.Join(dir, LocalFile), &local); err != nil {
		return t, err
	}
	switch {
	case hasSpec:
		t.Name = spec.Metadata.Name
	case hasOld && old.Metadata.Name != "":
		t.Name = old.Metadata.Name
	case hasOld && old.Name != "":
		t.Name = old.Name
	case hasOld:
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
