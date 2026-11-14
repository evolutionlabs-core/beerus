package bank_test

import (
	"os/exec"
	"strings"
	"testing"
)

const (
	modulePath     = "github.com/evolutionlabs-core/beerus"
	testSupportPkg = modulePath + "/internal/bank/banktest"
)

func TestImportBoundary(t *testing.T) {
	forbidden := []string{
		modulePath + "/internal/nibss",
		modulePath + "/internal/connect",
		modulePath + "/cmd",
	}
	out, err := exec.Command("go", "list", "-f", `{{.ImportPath}} {{join .Deps " "}}`, "./...").Output()
	if err != nil {
		t.Fatalf("list bank packages: %v", err)
	}
	listed := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] == testSupportPkg {
			continue
		}
		listed++
		for _, dep := range fields[1:] {
			for _, bad := range forbidden {
				if dep == bad || strings.HasPrefix(dep, bad+"/") {
					t.Errorf("%s imports %s", fields[0], dep)
				}
			}
		}
	}
	if listed == 0 {
		t.Fatal("no bank packages found")
	}
}
