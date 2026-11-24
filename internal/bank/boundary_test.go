package bank_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const modulePath = "github.com/evolutionlabs-core/beerus"

var forbidden = []string{modulePath + "/internal/nibss", modulePath + "/internal/core", modulePath + "/internal/setup", modulePath + "/cmd"}

func goList(t *testing.T, args ...string) []string {
	t.Helper()
	out, err := exec.Command("go", append([]string{"list"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("list %v: %v: %s", args, err, out)
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

func TestBoundary(t *testing.T) {
	t.Parallel()
	listed := map[string]bool{}
	for _, line := range goList(t, "-f", `{{.ImportPath}} {{join .Deps " "}} {{join .TestImports " "}} {{join .XTestImports " "}}`, "./...", "../web", "../view") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] == modulePath+"/internal/bank/banktest" {
			continue
		}
		listed[fields[0]] = true
		for _, dep := range fields[1:] {
			if bad := reaches(dep); bad != "" {
				t.Errorf("%s imports %s", fields[0], dep)
			}
		}
	}
	for _, name := range []string{"bank/consent", "bank/participant", "bank/server", "bank/site", "bank/store", "view", "web"} {
		if !listed[modulePath+"/internal/"+name] {
			t.Errorf("internal/%s is not covered by the boundary check", name)
		}
	}
	deps := goList(t, "-deps", "./participant")
	if !slices.Contains(deps, modulePath+"/internal/bank/site") || !slices.Contains(deps, modulePath+"/internal/bank/consent") {
		t.Fatalf("the bank process does not reach its site and consent sender: %v", deps)
	}
	for _, dep := range append(deps, goList(t, "-deps", "../view")...) {
		if bad := reaches(dep); bad != "" {
			t.Errorf("a bank process package reaches %s through %s", bad, dep)
		}
	}
}
