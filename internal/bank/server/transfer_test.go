package server

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"github.com/evolutionlabs-core/beerus/internal/nip"
)

func TestTransferRequestsAndRoutes(t *testing.T) {
	a := &config.Bank{ID: "bank-a", Name: "Bank A", NIPCode: "999001"}
	b := &config.Bank{ID: "bank-b", Name: "Bank B", NIPCode: "999002"}
	req := control.TransferRequest{FromBank: "bank-a", ToBank: "bank-b", Amount: 100}
	closure := req
	closure.Closure = true
	act, sus, clo := config.PhaseActive, config.PhaseSuspended, config.PhaseClosed
	for _, r := range []struct {
		name       string
		req        control.TransferRequest
		from, to   *config.Bank
		fromP, toP string
		want       string
	}{
		{"open", req, a, b, act, act, "999002"},
		{"suspended receiver still routes", req, a, b, act, sus, "999002"},
		{"closure ignores the sender phase", closure, a, b, config.PhaseClosing, act, "999002"},
		{"unknown sender", req, nil, b, "", act, `404 not_found no bank "bank-a"`},
		{"unknown receiver", req, a, nil, act, "", `422 unknown_bank find bank "bank-b": not in the NIBSS directory`},
		{"sender not active", req, a, b, sus, act, "409 bank_not_active Bank A is suspended, so it cannot do this now"},
		{"receiver closed", req, a, b, act, clo, "422 bank_closed Bank B is closed and takes no more transfers. Nothing was debited"},
	} {
		rt, err := NewRoute(r.req, r.from, r.to, r.fromP, r.toP)
		got, te := rt.ToCode, (*control.TransferError)(nil)
		if errors.As(err, &te) {
			got = fmt.Sprintf("%d %s %s", te.Status, te.ErrCode, te.Error())
		}
		if got != r.want {
			t.Errorf("%s: %q, want %q", r.name, got, r.want)
		}
	}
	for _, c := range []struct {
		name      string
		amount    int64
		narration string
		to        string
		want      string
	}{
		{"zero", 0, "", "0000000012", "400 " + nip.SystemError + " amount must be above zero"},
		{"negative", -5, "", "0000000012", "400 " + nip.SystemError + " amount must be above zero"},
		{"long narration", 1, strings.Repeat("a", 101), "0000000012", "422 " + nip.SystemError + " narration has 101 characters; the limit is 100"},
		{"control character", 1, "a\tb", "0000000012", "422 " + nip.SystemError + " narration must not contain control characters"},
		{"letters in the account", 1, "", "00000000AB", "422 " + nip.InvalidAccount + " that is not a valid account number, so nothing was debited"},
		{"narration at the limit", 1, strings.Repeat("a", 100), "0000000012", ""},
	} {
		got, te := "", (*control.TransferError)(nil)
		if err := CheckRequest(control.TransferRequest{Amount: c.amount, Narration: c.narration, To: c.to}); errors.As(err, &te) {
			got = fmt.Sprintf("%d %s %s", te.Status, te.Code, te.Error())
		}
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
