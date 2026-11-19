package server

import (
	"context"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"net/http"

	"github.com/evolutionlabs-core/beerus/internal/web"
)

func (s *Banking) LedgerCheck(ctx context.Context, bankID string) (control.LedgerCheck, error) {
	bk, err := s.bankBook(bankID)
	if err != nil {
		return control.LedgerCheck{}, err
	}
	c, err := bk.Bank.L.Verify(ctx)
	if err != nil {
		return control.LedgerCheck{}, err
	}
	out := control.LedgerCheck{OK: c.OK(), TrialBalance: c.TrialBalance, BalanceMismatches: []string{}, EventMismatches: []string{}, UnbalancedEntries: []int64{}}
	out.BalanceMismatches = append(out.BalanceMismatches, c.BalanceMismatch...)
	out.EventMismatches = append(out.EventMismatches, c.EventMismatch...)
	out.UnbalancedEntries = append(out.UnbalancedEntries, c.UnbalancedEntries...)
	return out, nil
}

func (s *Banking) HandleLedgerCheck(w http.ResponseWriter, r *http.Request) {
	out, err := s.LedgerCheck(r.Context(), s.bankIDOf(r))
	if err != nil {
		s.Error(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, out)
}
