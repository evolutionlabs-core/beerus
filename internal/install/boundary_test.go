package install_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const (
	module   = "github.com/evolutionlabs-core/beerus"
	install  = module + "/internal/install"
	core     = module + "/internal/core"
	command  = module + "/cmd/beerus"
	contract = module + "/contracttest"
)

func goList(t *testing.T, args ...string) []string {
	t.Helper()
	out, err := exec.Command("go", append([]string{"list"}, args...)...).Output()
	if err != nil {
		t.Fatalf("list %v: %v", args, err)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

func TestOnlyTheCommandImportsInstall(t *testing.T) {
	listed := map[string]bool{}
	for _, line := range goList(t, "-f", `{{.ImportPath}}|{{join .Imports " "}}|{{join .TestImports " "}} {{join .XTestImports " "}}`, module+"/...") {
		f := strings.Split(line, "|")
		listed[f[0]] = true
		if f[0] == command || f[0] == install {
			continue
		}
		if slices.Contains(strings.Fields(f[1]), install) {
			t.Errorf("%s imports %s; only %s may", f[0], install, command)
		}
		if f[0] != contract && slices.Contains(strings.Fields(f[2]), install) {
			t.Errorf("the tests of %s import %s; only %s and %s may", f[0], install, command, contract)
		}
	}
	for _, p := range []string{command, core, install, contract} {
		if !listed[p] {
			t.Errorf("%s is not covered by the boundary check", p)
		}
	}
	if slices.Contains(goList(t, "-deps", core), install) || slices.Contains(goList(t, "-deps", install), core) || !slices.Contains(goList(t, "-deps", command), install) {
		t.Errorf("%s must not reach %s or the reverse, and %s must reach %s", core, install, command, install)
	}
}
