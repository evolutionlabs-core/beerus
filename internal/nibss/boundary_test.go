package nibss_test

import (
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const modulePath = "github.com/evolutionlabs-core/beerus"

var forbidden = []string{
	modulePath + "/internal/bank",
	modulePath + "/internal/core",
	modulePath + "/internal/setup",
	modulePath + "/cmd",
}

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

func reaches(dep string) string {
	for _, bad := range forbidden {
		if dep == bad || strings.HasPrefix(dep, bad+"/") {
			return bad
		}
	}
	return ""
}

func TestNIBSSProcessDeps(t *testing.T) {
	deps := goList(t, "-deps", "./participant")
	if !slices.Contains(deps, modulePath+"/internal/nibss/gateway") || !slices.Contains(deps, modulePath+"/internal/nibss") {
		t.Fatalf("the NIBSS process does not reach its gateway and store: %v", deps)
	}
	for _, dep := range deps {
		if bad := reaches(dep); bad != "" {
			t.Errorf("the NIBSS process reaches %s through %s", bad, dep)
		}
	}
	lines := goList(t, "-f", `{{.ImportPath}} {{join .Deps " "}} {{join .TestImports " "}} {{join .XTestImports " "}}`, "./...")
	listed := map[string]bool{}
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		listed[fields[0]] = true
		for _, dep := range fields[1:] {
			if bad := reaches(dep); bad != "" {
				t.Errorf("%s imports %s", fields[0], dep)
			}
		}
	}
	for _, name := range []string{"gateway", "participant"} {
		if !listed[modulePath+"/internal/nibss/"+name] {
			t.Errorf("internal/nibss/%s is not covered by the boundary check", name)
		}
	}
}
