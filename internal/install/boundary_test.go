package install_test

import (
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const (
	modulePath   = "github.com/evolutionlabs-core/beerus"
	installPath  = modulePath + "/internal/install"
	corePath     = modulePath + "/internal/core"
	cmdPath      = modulePath + "/cmd/beerus"
	contractPath = modulePath + "/contracttest"
)

func goList(t *testing.T, args ...string) []string {
	t.Helper()
	out, err := exec.Command("go", append([]string{"list"}, args...)...).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			t.Fatalf("list %v: %v: %s", args, err, exit.Stderr)
		}
		t.Fatalf("list %v: %v", args, err)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

func TestOnlyTheCommandImportsInstall(t *testing.T) {
	lines := goList(t, "-f", `{{.ImportPath}}|{{join .Imports " "}}|{{join .TestImports " "}}|{{join .XTestImports " "}}`, modulePath+"/...")
	listed := map[string]bool{}
	for _, line := range lines {
		fields := strings.Split(line, "|")
		if len(fields) != 4 {
			t.Fatalf("unexpected list line %q", line)
		}
		pkg := fields[0]
		listed[pkg] = true
		if pkg == cmdPath || pkg == installPath {
			continue
		}
		if slices.Contains(strings.Fields(fields[1]), installPath) {
			t.Errorf("%s imports %s; only %s may", pkg, installPath, cmdPath)
		}
		tests := slices.Concat(strings.Fields(fields[2]), strings.Fields(fields[3]))
		if slices.Contains(tests, installPath) && pkg != contractPath {
			t.Errorf("the tests of %s import %s; only %s and %s may", pkg, installPath, cmdPath, contractPath)
		}
	}
	for _, p := range []string{cmdPath, corePath, installPath, contractPath} {
		if !listed[p] {
			t.Errorf("%s is not covered by the boundary check", p)
		}
	}
}

func TestControlPlaneDoesNotReachInstall(t *testing.T) {
	for _, dep := range goList(t, "-deps", corePath) {
		if dep == installPath {
			t.Errorf("%s reaches %s", corePath, installPath)
		}
	}
	for _, dep := range goList(t, "-deps", installPath) {
		if dep == corePath {
			t.Errorf("%s reaches %s", installPath, corePath)
		}
	}
	if !slices.Contains(goList(t, "-deps", cmdPath), installPath) {
		t.Errorf("%s does not reach %s", cmdPath, installPath)
	}
}
