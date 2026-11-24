package core

import (
	"os/exec"
	"strings"
	"testing"
)

const modulePath = "github.com/evolutionlabs-core/beerus/internal/"

func TestCoreReachesNoParticipantAndImportsNoBankOrNIBSS(t *testing.T) {
	list := func(field string) []string {
		out, err := exec.Command("go", "list", "-f", `{{join .`+field+` " "}}`, ".").Output()
		if err != nil {
			t.Fatalf("list %s: %v", field, err)
		}
		return strings.Fields(string(out))
	}
	for _, dep := range list("Deps") {
		for _, banned := range []string{"bank/participant", "nibss/participant", "cbn/participant", "aggregator"} {
			if dep == modulePath+banned {
				t.Errorf("internal/core reaches %s", dep)
			}
		}
	}
	for _, imp := range list("Imports") {
		if strings.HasPrefix(imp, modulePath+"bank") || strings.HasPrefix(imp, modulePath+"nibss") {
			t.Errorf("internal/core imports %s", imp)
		}
	}
}
