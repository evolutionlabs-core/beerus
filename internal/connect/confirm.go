package connect

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

type bankConfirm struct {
	generation int64
	done       bool
	attempts   int
	next       time.Time
}

type confirmJob struct {
	b        config.Bank
	gen      int64
	attempts int
}

func (a *App) confirmDue(ctx context.Context) {
	if a.role != RoleCore {
		return
	}
	stored, err := a.ctl.status()
	if err != nil {
		a.log.Error("read control.json", "err", err)
		return
	}
	now := a.clock.Real()
	var jobs []confirmJob
	a.lifeMu.Lock()
	a.mu.Lock()
	for id, c := range a.confirms {
		if r := findResource(&stored, setup.KindBank, id); a.cfg.Bank(id) == nil || r == nil || r.Generation != c.generation {
			delete(a.confirms, id)
		}
	}
	for _, b := range a.cfg.Banks {
		st, r := a.status[b.ID], findResource(&stored, setup.KindBank, b.ID)
		if st == nil || r == nil || (st.Phase != PhaseProvisioning && st.Phase != PhaseRepairing) || !st.Steps[stepIndex(config.StepSite)].Done || !st.Steps[stepIndex(config.StepAPI)].Done {
			continue
		}
		c := a.confirms[b.ID]
		if c != nil && (c.done || now.Before(c.next)) {
			continue
		}
		job := confirmJob{b: *b, gen: r.Generation}
		if c != nil {
			job.attempts = c.attempts
		}
		jobs = append(jobs, job)
	}
	a.mu.Unlock()
	a.lifeMu.Unlock()
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.recordConfirm(j, a.confirmBank(ctx, &j.b))
		}()
	}
	wg.Wait()
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
	defer a.lifeMu.Unlock()
	if a.confirms == nil {
		a.confirms = map[string]*bankConfirm{}
	}
	c := &bankConfirm{generation: j.gen, done: err == nil, attempts: j.attempts}
	if err != nil {
		c.attempts++
		c.next = now.Add(min(confirmRetry*time.Duration(c.attempts), pushRetryMax))
	}
	a.confirms[j.b.ID] = c
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
