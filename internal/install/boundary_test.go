package install_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const (
	module  = "github.com/evolutionlabs-core/beerus"
	install = module + "/internal/install"
	core    = module + "/internal/core"
	cmd     = module + "/cmd/beerus"
)

func deps(t *testing.T, pkg string) []string {
	t.Helper()
	out, err := exec.Command("go", "list", "-deps", pkg).Output()
	if err != nil {
		t.Fatalf("list %s: %v", pkg, err)
	}
	return strings.Fields(string(out))
}

func TestInstallSitsBetweenTheCommandAndTheCore(t *testing.T) {
	if slices.Contains(deps(t, core), install) || slices.Contains(deps(t, install), core) {
		t.Errorf("%s and %s reach each other", core, install)
	}
	if !slices.Contains(deps(t, cmd), install) {
		t.Errorf("%s does not reach %s", cmd, install)
	}
}
