package connect

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/state"
)

const confirmTimeout = 2 * time.Second

type bankConfirm struct {
	generation int64
	token      string
	inflight   bool
	done       bool
	attempts   int
	next       time.Time
}

type confirmJob struct {
	b     config.Bank
	gen   int64
	token string
}

func (a *App) confirmDue(ctx context.Context) {
	if a.role != RoleCore {
		return
	}
	stored, err := a.ctl.Status()
	if err != nil {
		a.log.Error("read control.json", "err", err)
		return
	}
	now := a.clock.Real()
	var jobs []confirmJob
	a.lifeMu.Lock()
	a.mu.Lock()
	for id, c := range a.confirms {
		if r := state.FindResource(&stored, setup.KindBank, id); a.cfg.Bank(id) == nil || r == nil || r.Generation != c.generation {
			delete(a.confirms, id)
		}
	}
	for _, b := range a.cfg.Banks {
		st, r := a.status[b.ID], state.FindResource(&stored, setup.KindBank, b.ID)
		if st == nil || r == nil || (st.Phase != PhaseProvisioning && st.Phase != PhaseRepairing) || !st.Steps[stepIndex(config.StepSite)].Done || !st.Steps[stepIndex(config.StepAPI)].Done {
			continue
		}
		c := a.confirms[b.ID]
		if c != nil && (c.done || c.inflight || now.Before(c.next)) {
			continue
		}
		token, err := confirmToken()
		if err != nil {
			a.log.Error("confirm token", "bank", b.ID, "err", err)
			continue
		}
		if a.confirms == nil {
			a.confirms = map[string]*bankConfirm{}
		}
		next := &bankConfirm{generation: r.Generation, token: token, inflight: true}
		if c != nil {
			next.attempts = c.attempts
		}
		a.confirms[b.ID] = next
		jobs = append(jobs, confirmJob{b: *b, gen: r.Generation, token: token})
	}
	a.mu.Unlock()
	a.lifeMu.Unlock()
	for _, j := range jobs {
		a.confirmWG.Add(1)
		go func() {
			defer a.confirmWG.Done()
			a.recordConfirm(j, a.confirmBank(ctx, &j.b))
		}()
	}
}

func confirmToken() (string, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func (a *App) confirmBank(ctx context.Context, b *config.Bank) error {
	ctx, cancel := context.WithTimeout(ctx, confirmTimeout)
	defer cancel()
	w, err := a.wishFor(b.ID)
	if err != nil {
		return err
	}
	w.phase, w.reason = b.RunningPhase(), ""
	if err := a.syncBank(ctx, b.ID, b.URL(), wishGeneration(a.WorldID(), w), w); err != nil {
		return fmt.Errorf("confirm %s: %w", b.ID, err)
	}
	return nil
}

func (a *App) recordConfirm(j confirmJob, err error) {
	now := a.clock.Real()
	a.lifeMu.Lock()
	c := a.confirms[j.b.ID]
	if c == nil || c.token != j.token || c.generation != j.gen {
		a.lifeMu.Unlock()
		return
	}
	c.inflight = false
	if err == nil {
		c.done = true
		a.lifeMu.Unlock()
		return
	}
	c.attempts++
	c.next = now.Add(min(clock.Backoff(c.attempts), pushRetryMax))
	a.lifeMu.Unlock()
	a.log.Warn("bank not confirmed; retrying", "bank", j.b.ID, "err", err)
}

func (a *App) takeConfirm(id string, gen int64) bool {
	if a.role != RoleCore {
		return true
	}
	a.lifeMu.Lock()
	defer a.lifeMu.Unlock()
	c := a.confirms[id]
	if c == nil || !c.done || c.generation != gen {
		return false
	}
	delete(a.confirms, id)
	return true
}
