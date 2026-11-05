package connect

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

var ErrRunning = errors.New("Beerus is running; stop it first")

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
