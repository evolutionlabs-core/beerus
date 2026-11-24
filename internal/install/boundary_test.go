package install_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func deps(t *testing.T, pkg string) []string {
	t.Helper()
	out, err := exec.Command("go", "list", "-deps", "github.com/evolutionlabs-core/beerus/"+pkg).Output()
	if err != nil {
		t.Fatalf("list %s: %v", pkg, err)
	}
	return strings.Fields(string(out))
}

func TestInstallSitsBetweenTheCommandAndTheCore(t *testing.T) {
	const install, core = "github.com/evolutionlabs-core/beerus/internal/install", "github.com/evolutionlabs-core/beerus/internal/core"
	if slices.Contains(deps(t, "internal/core"), install) || slices.Contains(deps(t, "internal/install"), core) || !slices.Contains(deps(t, "cmd/beerus"), install) {
		t.Errorf("%s must not reach %s or the reverse, and the command must reach install", core, install)
	}
}
