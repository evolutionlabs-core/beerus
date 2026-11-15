package server

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nip"
)

func TestTransferRequestsAndRoutes(t *testing.T) {
	a := &config.Bank{ID: "bank-a", Name: "Bank A", NIPCode: "999001"}
	b := &config.Bank{ID: "bank-b", Name: "Bank B", NIPCode: "999002"}
	req := TransferRequest{FromBank: "bank-a", ToBank: "bank-b", Amount: 100}
	rows := []struct {
		name       string
		req        TransferRequest
		from, to   *config.Bank
		fromPhase  string
		toPhase    string
		status     int
		errCode    string
		message    string
		wantToCode string
	}{
		{name: "open", req: req, from: a, to: b, fromPhase: config.PhaseActive, toPhase: config.PhaseActive, wantToCode: "999002"},
		{name: "suspended receiver still routes", req: req, from: a, to: b, fromPhase: config.PhaseActive, toPhase: config.PhaseSuspended, wantToCode: "999002"},
		{name: "unknown sender", req: req, to: b, toPhase: config.PhaseActive, status: http.StatusNotFound, errCode: "not_found", message: `no bank "bank-a"`},
		{name: "unknown receiver", req: req, from: a, fromPhase: config.PhaseActive, status: http.StatusUnprocessableEntity, errCode: "unknown_bank", message: `find bank "bank-b": not in the NIBSS directory`},
		{name: "sender not active", req: req, from: a, to: b, fromPhase: config.PhaseSuspended, toPhase: config.PhaseActive, status: http.StatusConflict, errCode: "bank_not_active", message: "Bank A is suspended, so it cannot do this now"},
		{name: "closure ignores the sender phase", req: TransferRequest{FromBank: "bank-a", ToBank: "bank-b", Amount: 100, Closure: true}, from: a, to: b, fromPhase: config.PhaseClosing, toPhase: config.PhaseActive, wantToCode: "999002"},
		{name: "receiver closed", req: req, from: a, to: b, fromPhase: config.PhaseActive, toPhase: config.PhaseClosed, status: http.StatusUnprocessableEntity, errCode: "bank_closed", message: "Bank B is closed and takes no more transfers. Nothing was debited"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			rt, err := NewRoute(r.req, r.from, r.to, r.fromPhase, r.toPhase)
			if r.errCode == "" {
				if err != nil || rt.ToCode != r.wantToCode || rt.FromBank != "bank-a" {
					t.Fatalf("route %+v, error %v", rt, err)
				}
				return
			}
			var te *TransferError
			if !errors.As(err, &te) || te.Status != r.status || te.ErrCode != r.errCode || te.Error() != r.message {
				t.Fatalf("error %v, want %d %s %q", err, r.status, r.errCode, r.message)
			}
		})
	}
	checks := []struct {
		name      string
		amount    int64
		narration string
		to        string
		message   string
		status    int
	}{
		{"zero", 0, "", "0000000012", "amount must be above zero", http.StatusBadRequest},
		{"negative", -5, "", "0000000012", "amount must be above zero", http.StatusBadRequest},
		{"long narration", 1, strings.Repeat("a", 101), "0000000012", "narration has 101 characters; the limit is 100", http.StatusUnprocessableEntity},
		{"control character", 1, "a\tb", "0000000012", "narration must not contain control characters", http.StatusUnprocessableEntity},
		{"short account", 1, "", "123456789", "that is not a valid account number, so nothing was debited", http.StatusUnprocessableEntity},
		{"letters in the account", 1, "", "00000000AB", "that is not a valid account number, so nothing was debited", http.StatusUnprocessableEntity},
		{"fine", 1, strings.Repeat("a", 100), "0000000012", "", 0},
	}
	for _, c := range checks {
		err := CheckRequest(TransferRequest{Amount: c.amount, Narration: c.narration, To: c.to})
		var te *TransferError
		switch {
		case c.message == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.message != "" && (!errors.As(err, &te) || te.Status != c.status || te.Error() != c.message || te.Code != wantCode(c.message)):
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func wantCode(message string) string {
	if strings.Contains(message, "account number") {
		return nip.InvalidAccount
	}
	return nip.SystemError
}
