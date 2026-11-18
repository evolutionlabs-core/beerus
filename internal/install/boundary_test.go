package install_test

import (
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const (
	modulePath  = "github.com/evolutionlabs-core/beerus"
	installPath = modulePath + "/internal/install"
	connectPath = modulePath + "/internal/connect"
	cmdPath     = modulePath + "/cmd/beerus"
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
	lines := goList(t, "-f", `{{.ImportPath}} {{join .Imports " "}}`, modulePath+"/...")
	listed := map[string]bool{}
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		listed[fields[0]] = true
		if fields[0] == cmdPath || fields[0] == installPath {
			continue
		}
		if slices.Contains(fields[1:], installPath) {
			t.Errorf("%s imports %s; only %s may", fields[0], installPath, cmdPath)
		}
	}
	for _, p := range []string{cmdPath, connectPath, installPath} {
		if !listed[p] {
			t.Errorf("%s is not covered by the boundary check", p)
		}
	}
}

func TestControlPlaneDoesNotReachInstall(t *testing.T) {
	for _, dep := range goList(t, "-deps", connectPath) {
		if dep == installPath {
			t.Errorf("%s reaches %s", connectPath, installPath)
		}
	}
	for _, dep := range goList(t, "-deps", installPath) {
		if dep == connectPath {
			t.Errorf("%s reaches %s", installPath, connectPath)
		}
	}
	if !slices.Contains(goList(t, "-deps", cmdPath), installPath) {
		t.Errorf("%s does not reach %s", cmdPath, installPath)
	}
}
