package connect

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/view"
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

func (a *App) settleInflight(ctx context.Context, r setup.Resource) (*setup.Condition, error) {
	return a.settleWait(ctx, r)
}

func (a *App) settleMoves(ctx context.Context, r setup.Resource) (*setup.Condition, error) {
	cond, err := a.settleWait(ctx, r)
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

func (a *App) settleWait(ctx context.Context, r setup.Resource) (*setup.Condition, error) {
	if a.wantsSettleNow(r) {
		if _, err := a.settle(a.clock.Now(), true); err != nil {
			return nil, fmt.Errorf("run the extra settlement session: %w", err)
		}
	}
	sends, cond, err := a.recordedSends(r.ID)
	if cond != nil || err != nil {
		return cond, err
	}
	msg, err := a.inflightWith(ctx, r.Allocated.NIPCode, r.ID, sends)
	if err != nil || msg == "" {
		return nil, err
	}
	return a.blocked(setup.ReasonSettlementPending, "%s", msg), nil
}

const sendReadTTL = time.Minute

type sendRead struct {
	open int
	at   time.Time
	err  error
}

func (a *App) openSends(ctx context.Context, b *config.Bank) (int, error) {
	if a.role != RoleCore {
		a.mu.Lock()
		ls := a.ledgerFor(b.ID)
		a.mu.Unlock()
		if ls == nil {
			return 0, fmt.Errorf("the books of %s are not open", b.Name)
		}
		return ls.Bank().OpenSends(ctx)
	}
	if !pushable(a.bankPhase(b.ID)) {
		return 0, nil
	}
	var out server.OpenSends
	if _, err := a.link.Request(ctx, b.URL(), http.MethodGet, "/v1/nip/sends/open", nil, nil, &out); err != nil {
		return 0, fmt.Errorf("count open sends of %s: %w", b.ID, err)
	}
	return out.Open, nil
}

func (a *App) sendsDue(ctx context.Context) {
	st, err := a.ctl.status()
	if err != nil {
		a.log.Error("read control.json", "err", err)
		return
	}
	now := a.clock.Real()
	for _, r := range st.Resources {
		if r.Kind != setup.KindBank || (r.Phase != config.PhaseClosing && r.Phase != config.PhaseTerminating) || (r.NextAttemptAt != nil && now.Before(*r.NextAttemptAt)) {
			continue
		}
		switch nextFinalizer(r) {
		case setup.FinalizerSettleInflight, setup.FinalizerSettleMoves, setup.FinalizerPurgeOwned:
		default:
			continue
		}
		b := a.bankByID(r.ID)
		if b == nil {
			continue
		}
		n, err := a.openSends(ctx, b)
		a.mu.Lock()
		if a.sendReads == nil {
			a.sendReads = map[string]sendRead{}
		}
		a.sendReads[r.ID] = sendRead{open: n, at: a.clock.Real(), err: err}
		a.mu.Unlock()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, read := range a.sendReads {
		if a.clock.Real().Sub(read.at) > sendReadTTL {
			delete(a.sendReads, id)
		}
	}
}

func (a *App) recordedSends(id string) (int, *setup.Condition, error) {
	a.mu.Lock()
	read, ok := a.sendReads[id]
	a.mu.Unlock()
	if !ok || a.clock.Real().Sub(read.at) > sendReadTTL {
		return 0, a.blocked(setup.ReasonSettlementPending, "Checking the transfers %s is still sending. Try again soon.", id), nil
	}
	return read.open, nil, read.err
}

func (a *App) inflightWith(ctx context.Context, code, id string, sends int) (string, error) {
	if sends > 0 {
		name := id
		if b := a.bankByID(id); b != nil {
			name = b.Name
		}
		return fmt.Sprintf("%s has %s still being sent. Wait for them to finish.", name, view.Plural(sends, "transfer", "transfers")), nil
	}
	sw := a.nibssStore()
	if sw == nil {
		return "", errors.New("NIBSS is not open, so open NIP sessions cannot be checked; try again soon")
	}
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
		if x.ToCode == code && x.Status == nibss.SessionOK {
			net += x.Amount
		}
	}
	if transfers > 0 {
		cutoff := a.schedule().next(a.clock.Now()).In(clock.Lagos).Format("15:04")
		return fmt.Sprintf("Waiting for the %s NIP session to settle %s (net %s). Wait for it, or run beerus nip settle-now.", cutoff, view.Plural(transfers, "transfer", "transfers"), bank.Naira(max(net, -net))), nil
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
		return fmt.Sprintf("Waiting for %s with %s to be posted to the books. Try again soon.", view.Plural(waiting, "settlement", "settlements"), id), nil
	}
	return "", nil
}
