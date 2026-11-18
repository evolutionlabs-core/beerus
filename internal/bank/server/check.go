package server

import (
	"context"
	"net/http"

	"github.com/evolutionlabs-core/beerus/internal/web"
)

type LedgerCheck struct {
	OK                bool     `json:"ok"`
	TrialBalance      int64    `json:"trial_balance_minor"`
	BalanceMismatches []string `json:"balance_mismatches"`
	EventMismatches   []string `json:"event_mismatches"`
	UnbalancedEntries []int64  `json:"unbalanced_entries"`
}

func (s *Banking) LedgerCheck(ctx context.Context, bankID string) (LedgerCheck, error) {
	bk, err := s.bankBook(bankID)
	if err != nil {
		return LedgerCheck{}, err
	}
	c, err := bk.Bank.L.Verify(ctx)
	if err != nil {
		return LedgerCheck{}, err
	}
	out := LedgerCheck{OK: c.OK(), TrialBalance: c.TrialBalance, BalanceMismatches: []string{}, EventMismatches: []string{}, UnbalancedEntries: []int64{}}
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
