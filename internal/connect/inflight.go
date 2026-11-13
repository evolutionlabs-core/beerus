package connect

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func (a *App) markSettleNow(kind, id string) {
	key := resourceKey(setup.Resource{Kind: kind, ID: id})
	a.mu.Lock()
	defer a.mu.Unlock()
	if !slices.Contains(a.st.SettleNow, key) {
		a.st.SettleNow = append(a.st.SettleNow, key)
		a.dirty()
	}
}

func (a *App) clearSettleNow(kind, id string) {
	key := resourceKey(setup.Resource{Kind: kind, ID: id})
	a.mu.Lock()
	defer a.mu.Unlock()
	if slices.Contains(a.st.SettleNow, key) {
		a.st.SettleNow = slices.DeleteFunc(a.st.SettleNow, func(k string) bool { return k == key })
		a.dirty()
	}
}

func (a *App) wantsSettleNow(r setup.Resource) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Contains(a.st.SettleNow, resourceKey(r))
}

func (a *App) settleInflight(_ context.Context, r setup.Resource) (*setup.Condition, error) {
	return a.settleWait(r)
}

func (a *App) settleMoves(_ context.Context, r setup.Resource) (*setup.Condition, error) {
	cond, err := a.settleWait(r)
	if cond != nil || err != nil {
		return cond, err
	}
	a.mu.Lock()
	ls := a.ledgerFor(r.ID)
	a.mu.Unlock()
	if ls == nil {
		return nil, fmt.Errorf("the books of %s are not open", r.ID)
	}
	books, err := a.settlementBooks()
	if err != nil {
		return nil, err
	}
	problems, err := books.check(ls)
	if err != nil {
		return nil, fmt.Errorf("check the books of %s: %w", r.ID, err)
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("the books of %s disagree with NIBSS and the CBN: %s", r.ID, strings.Join(problems, "; "))
	}
	return nil, nil
}

func (a *App) settleWait(r setup.Resource) (*setup.Condition, error) {
	if a.wantsSettleNow(r) {
		if _, err := a.settleGated(a.clock.Now(), true); err != nil {
			return nil, fmt.Errorf("run the extra settlement session: %w", err)
		}
	}
	msg, err := a.inflightWith(r.Allocated.NIPCode, r.ID)
	if err != nil || msg == "" {
		return nil, err
	}
	return a.blocked(setup.ReasonSettlementPending, "%s", msg), nil
}

func (a *App) inflightWith(code, id string) (string, error) {
	sw := a.nibssStore()
	if sw == nil {
		return "", errors.New("NIBSS is not open, so open NIP sessions cannot be checked; try again soon")
	}
	ctx := context.Background()
	open, err := sw.Open(ctx)
	if err != nil {
		return "", fmt.Errorf("check open NIP sessions: %w", err)
	}
	transfers := 0
	var net int64
	for _, x := range open {
		if x.FromCode != code && x.ToCode != code {
			continue
		}
		transfers++
		if x.FromCode == code {
			net -= x.Amount
		}
		if x.ToCode == code && (x.Status == nibss.SessionOK || x.Outcome == nibss.OutcomeCredited) {
			net += x.Amount
		}
	}
	if transfers > 0 {
		cutoff := a.schedule().next(a.clock.Now()).In(clock.Lagos).Format("15:04")
		return fmt.Sprintf("Waiting for the %s NIP session to settle %s (net %s). Wait for it, or run beerus nip settle-now.", cutoff, plural(transfers, "transfer", "transfers"), naira(max(net, -net))), nil
	}
	unposted, err := sw.Unposted(ctx)
	if err != nil {
		return "", fmt.Errorf("check unposted settlements: %w", err)
	}
	waiting := 0
	for _, s := range unposted {
		if slices.ContainsFunc(s.Sessions, func(x nibss.Session) bool { return x.FromCode == code || x.ToCode == code }) {
			waiting++
		}
	}
	if waiting > 0 {
		return fmt.Sprintf("Waiting for %s with %s to be posted to the books. Try again soon.", plural(waiting, "settlement", "settlements"), id), nil
	}
	return "", nil
}
