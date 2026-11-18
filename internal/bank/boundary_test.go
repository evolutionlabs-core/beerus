package bank_test

import (
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const (
	modulePath     = "github.com/evolutionlabs-core/beerus"
	testSupportPkg = modulePath + "/internal/bank/banktest"
)

var forbidden = []string{
	modulePath + "/internal/nibss",
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

func TestImportBoundary(t *testing.T) {
	lines := goList(t, "-f", `{{.ImportPath}} {{join .Deps " "}} {{join .TestImports " "}} {{join .XTestImports " "}}`, "./...", "../web", "../view")
	listed := map[string]bool{}
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] == testSupportPkg {
			continue
		}
		listed[fields[0]] = true
		for _, dep := range fields[1:] {
			if bad := reaches(dep); bad != "" {
				t.Errorf("%s imports %s", fields[0], dep)
			}
		}
	}
	for _, name := range []string{"consent", "participant", "server", "site", "store"} {
		if !listed[modulePath+"/internal/bank/"+name] {
			t.Errorf("internal/bank/%s is not covered by the boundary check", name)
		}
	}
	for _, name := range []string{"view", "web"} {
		if !listed[modulePath+"/internal/"+name] {
			t.Errorf("internal/%s is not covered by the boundary check", name)
		}
	}
}

func TestBankProcessDeps(t *testing.T) {
	deps := goList(t, "-deps", "./participant")
	if !slices.Contains(deps, modulePath+"/internal/bank/site") || !slices.Contains(deps, modulePath+"/internal/bank/consent") {
		t.Fatalf("the bank process does not reach its site and consent sender: %v", deps)
	}
	for _, dep := range deps {
		if bad := reaches(dep); bad != "" {
			t.Errorf("the bank process reaches %s through %s", bad, dep)
		}
	}
	for _, dep := range goList(t, "-deps", "../view") {
		if bad := reaches(dep); bad != "" {
			t.Errorf("the shared UI package reaches %s through %s", bad, dep)
		}
	}
}
