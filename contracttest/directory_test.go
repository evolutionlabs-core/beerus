package contracttest

import (
	"context"
	"slices"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func TestBanksJoinTheNIBSSDirectory(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newBankStack(t)
	name := config.DatabaseName(s.cfg.Dir, setup.SystemNIBSS)
	if !slices.Contains(s.owned(setup.KindSystem, setup.SystemNIBSS), setup.Owned{Kind: "database", Name: name}) {
		t.Fatalf("System/nibss does not own %s: %v", name, s.owned(setup.KindSystem, setup.SystemNIBSS))
	}
	st, err := setup.LoadStatus(s.cfg.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range st.Resources {
		if r.Kind == setup.KindBank && r.Steps[setup.StepDirectory] != "done" {
			t.Errorf("%s: directory step %q", r.ID, r.Steps[setup.StepDirectory])
		}
		if r.Kind == setup.KindSystem && r.ID == setup.SystemNIBSS && r.Phase != setup.PhaseActive {
			t.Errorf("System/nibss is %s", r.Phase)
		}
	}
	dir, err := nibss.Open(context.Background(), s.cfg.DatabaseURL, name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dir.Close)
	status := func(id string) string {
		t.Helper()
		list, err := dir.Directory(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, in := range list {
			if in.ID == id {
				return in.Status
			}
		}
		return ""
	}
	for _, b := range s.cfg.Banks {
		in, err := dir.Institution(context.Background(), b.NIPCode)
		if err != nil {
			t.Fatal(err)
		}
		if in.ID != b.ID || in.NUBANCode != b.NUBANCode || in.Status != nibss.StatusActive {
			t.Errorf("directory entry for %s: %+v", b.ID, in)
		}
	}
	s.ctl("POST", "/banks/bank-b/suspend", nil)
	if got := status("bank-b"); got != nibss.StatusSuspended {
		t.Fatalf("bank-b after suspend: %q", got)
	}
	s.ctl("POST", "/banks/bank-b/resume", nil)
	if got := status("bank-b"); got != nibss.StatusActive {
		t.Fatalf("bank-b after resume: %q", got)
	}
	found := false
	for _, p := range s.ctl("GET", "/nip/participants", nil)["data"].([]any) {
		m := p.(map[string]any)
		if m["bank_id"] == "bank-a" && m["status"] == nibss.StatusActive {
			found = true
		}
	}
	if !found {
		t.Fatal("bank-a is not a member in the NIP participant list")
	}
}
