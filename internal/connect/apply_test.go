package connect

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func TestApplyRefusals(t *testing.T) {
	a := &App{}
	a.applyMu.Lock()
	_, err := a.Apply(setup.Spec{}, ApplyOptions{})
	a.applyMu.Unlock()
	check := func(err error, status int, code, message string) {
		t.Helper()
		var ae *applyError
		if !errors.As(err, &ae) || ae.status != status || ae.code != code || ae.message != message {
			t.Errorf("got %v, want %d %s %q", err, status, code, message)
		}
	}
	check(err, http.StatusConflict, "apply_running", "Another apply is running. Try again when it finishes.")

	removes := func(ids ...string) setup.Plan {
		var p setup.Plan
		for i, id := range ids {
			kind := setup.KindBank
			if i > 0 {
				kind = setup.KindPartner
			}
			p.Actions = append(p.Actions, setup.Action{Op: setup.OpRemove, Kind: kind, ID: id})
		}
		return p
	}
	check(admissible(removes("bank-b"), ApplyOptions{}), http.StatusConflict, "prune_required", "The plan removes bank-b. Nothing was changed. Run again with --prune to remove it.")
	check(admissible(removes("bank-b", "ledgerly", "other"), ApplyOptions{Confirmed: []string{"bank-b"}}), http.StatusConflict, "prune_required", "The plan removes ledgerly and other. Nothing was changed. Run again with --prune to remove them.")
	if err := admissible(removes("bank-b", "ledgerly"), ApplyOptions{Confirmed: []string{"bank-b", "ledgerly"}}); err != nil {
		t.Errorf("every removal was confirmed: %v", err)
	}
	if err := admissible(removes("bank-b", "ledgerly"), ApplyOptions{Prune: true}); err != nil {
		t.Errorf("prune was set: %v", err)
	}
	blocked := setup.Plan{Actions: []setup.Action{{Op: setup.OpRemove, Kind: setup.KindBank, ID: "bank-a", Blockers: []string{"bank-c settles through bank-a and is not leaving."}}}}
	check(admissible(blocked, ApplyOptions{Prune: true}), http.StatusUnprocessableEntity, "invalid_spec", "bank-c settles through bank-a and is not leaving.")

	moved := &setup.Status{Resources: []setup.Resource{{Kind: setup.KindBank, ID: "bank-b", Phase: setup.PhaseTerminating, Done: []string{setup.FinalizerDecideMoney}}}}
	_, err = admit(moved, &admission{retarget: map[string]bool{"bank-b": true}})
	check(err, http.StatusConflict, "money_moved", "The money of bank-b has already moved. Nothing was changed.")

	plain := errors.New("setup.yaml:3:9: banks[bank-a].name is required. Add the bank's name.")
	check(planFailure([]error{plain, plain}), http.StatusUnprocessableEntity, "invalid_spec", plain.Error()+"\n"+plain.Error())
	check(planFailure([]error{plain, fmt.Errorf("moved: %w", setup.ErrBeingRemoved)}), http.StatusConflict, "being_removed", plain.Error()+"\nmoved: "+setup.ErrBeingRemoved.Error())
	check(planFailure([]error{fmt.Errorf("x: %w", setup.ErrBeingRemoved), fmt.Errorf("y: %w", setup.ErrSetupMismatch)}), http.StatusConflict, "setup_mismatch", "x: "+setup.ErrBeingRemoved.Error()+"\ny: "+setup.ErrSetupMismatch.Error())
}
