package core

import (
	"context"
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"github.com/evolutionlabs-core/beerus/internal/money"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
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
	read, ok := a.moveReads[r.ID]
	a.mu.Unlock()
	if !ok || a.clock.Real().Sub(read.at) > finalizerReadTTL {
		return a.blocked(setup.ReasonSettlementPending, "Checking the books of %s. Try again soon.", r.ID), nil
	}
	if read.err != nil {
		return nil, fmt.Errorf("check the books of %s: %w", r.ID, read.err)
	}
	if len(read.problems) > 0 {
		return nil, fmt.Errorf("the books of %s disagree with NIBSS and the CBN: %s", r.ID, strings.Join(read.problems, "; "))
	}
	return nil, nil
}

func (a *App) settleWait(ctx context.Context, r setup.Resource) (*setup.Condition, error) {
	sends, cond, err := a.recordedSends(r.ID)
	if cond != nil || err != nil {
		return cond, err
	}
	msg, err := a.inflightWith(ctx, r.ID, sends)
	if err != nil || msg == "" {
		return nil, err
	}
	return a.blocked(setup.ReasonSettlementPending, "%s", msg), nil
}

const finalizerReadTTL = time.Minute

type sendRead struct {
	open int
	at   time.Time
	err  error
}

type moveRead struct {
	problems []string
	at       time.Time
	err      error
}

func (a *App) openSends(ctx context.Context, b *config.Bank) (int, error) {
	if b.Closed() || !pushable(a.bankPhase(b.ID)) {
		return 0, nil
	}
	var out control.OpenSends
	if _, err := a.link.Request(ctx, b.URL(), http.MethodGet, "/v1/nip/sends/open", nil, nil, &out); err != nil {
		return 0, fmt.Errorf("count open sends of %s: %w", b.ID, err)
	}
	return out.Open, nil
}

func (a *App) sendsDue(ctx context.Context) {
	st, err := a.ctl.Status()
	if err != nil {
		a.log.Error("read control.json", "err", err)
		return
	}
	now := a.clock.Real()
	for _, r := range st.Resources {
		if r.Kind != setup.KindBank || (r.Phase != config.PhaseClosing && r.Phase != config.PhaseTerminating) || (r.NextAttemptAt != nil && now.Before(*r.NextAttemptAt)) {
			continue
		}
		fin := nextFinalizer(r)
		switch fin {
		case setup.FinalizerSettleInflight, setup.FinalizerSettleMoves, setup.FinalizerPurgeOwned:
		default:
			continue
		}
		b := a.bankByID(r.ID)
		if b == nil {
			continue
		}
		a.readSends(ctx, r, b, fin != setup.FinalizerPurgeOwned && a.wantsSettleNow(r))
		if fin == setup.FinalizerSettleMoves {
			a.readMoves(ctx, r.ID)
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, read := range a.sendReads {
		if a.clock.Real().Sub(read.at) > finalizerReadTTL {
			delete(a.sendReads, id)
		}
	}
	for id, read := range a.moveReads {
		if a.clock.Real().Sub(read.at) > finalizerReadTTL {
			delete(a.moveReads, id)
		}
	}
}

func (a *App) readSends(ctx context.Context, r setup.Resource, b *config.Bank, settleFirst bool) {
	var n int
	var err error
	if settleFirst {
		if _, serr := a.SettleNow(ctx); serr != nil {
			err = fmt.Errorf("run the extra settlement session: %w", serr)
		}
	}
	if err == nil {
		n, err = a.openSends(ctx, b)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sendReads == nil {
		a.sendReads = map[string]sendRead{}
	}
	a.sendReads[r.ID] = sendRead{open: n, at: a.clock.Real(), err: err}
}

func (a *App) readMoves(ctx context.Context, id string) {
	problems, err := a.moveProblems(ctx, id)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.moveReads == nil {
		a.moveReads = map[string]moveRead{}
	}
	a.moveReads[id] = moveRead{problems: problems, at: a.clock.Real(), err: err}
}

func (a *App) moveProblems(ctx context.Context, id string) ([]string, error) {
	return a.bankSettlementProblems(ctx, id)
}

func (a *App) recordedSends(id string) (int, *setup.Condition, error) {
	a.mu.Lock()
	read, ok := a.sendReads[id]
	a.mu.Unlock()
	if !ok || a.clock.Real().Sub(read.at) > finalizerReadTTL {
		return 0, a.blocked(setup.ReasonSettlementPending, "Checking the transfers %s is still sending. Try again soon.", id), nil
	}
	return read.open, nil, read.err
}

func (a *App) inflightWith(ctx context.Context, id string, sends int) (string, error) {
	if sends > 0 {
		name := id
		if b := a.bankByID(id); b != nil {
			name = b.Name
		}
		finish := "them"
		if sends == 1 {
			finish = "it"
		}
		return fmt.Sprintf("%s has %s still being sent. Wait for %s to finish.", name, view.Plural(sends, "transfer", "transfers"), finish), nil
	}
	if a.nibssCtl == nil {
		return "", &control.UnavailableError{Msg: nibssAway}
	}
	open, err := a.nibssCtl.openSessions(ctx, id)
	if err != nil {
		return "", err
	}
	if open.Transfers > 0 {
		net, ok := money.AddMinor(open.Inward, -open.Outward)
		if open.Inward < 0 || open.Outward < 0 || !ok {
			return "", fmt.Errorf("the open NIP sessions of %s do not add up", id)
		}
		next, err := a.nibssCtl.nextCutoff(ctx)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Waiting for the %s NIP session to settle %s (net %s). Wait for it, or run beerus nip settle-now.", next.In(clock.Lagos).Format("15:04"), view.Plural(open.Transfers, "transfer", "transfers"), money.Naira(max(net, -net))), nil
	}
	if open.Unposted > 0 {
		return fmt.Sprintf("Waiting for %s with %s to be posted to the books. Try again soon.", view.Plural(open.Unposted, "settlement", "settlements"), id), nil
	}
	return "", nil
}
