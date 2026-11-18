package participant

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

type ensureStep struct {
	id  string
	run func(ctx context.Context) (int, error)
}

func (p *Participant) booksNow() *store.Books {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.books
}

func (p *Participant) ensureSteps(b *config.Bank) []ensureStep {
	return []ensureStep{
		{config.StepDatabase, func(ctx context.Context) (int, error) {
			return 0, store.Create(ctx, p.cfg.DatabaseURL, b.Database)
		}},
		{config.StepLedgerSchema, func(ctx context.Context) (int, error) {
			return store.Migrate(ctx, p.cfg.DatabaseURL, b.Database, ledger.Migrate)
		}},
		{config.StepOpenBankingSchema, func(ctx context.Context) (int, error) {
			n, err := store.Migrate(ctx, p.cfg.DatabaseURL, b.Database, openbanking.Migrate)
			if err != nil {
				return 0, err
			}
			return n, p.openBooks(ctx, b)
		}},
	}
}

func (p *Participant) openBooks(ctx context.Context, b *config.Bank) error {
	if p.booksNow() != nil {
		return nil
	}
	inst := bank.Profile{ID: b.ID, Name: b.Name, NIPCode: b.NIPCode, NUBANCode: b.NUBANCode}
	books, err := store.OpenBooks(ctx, p.cfg.DatabaseURL, b.Database, inst, p.cfg.Fees(), p.clock.Now(), openbanking.Migrate)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.books = books
	p.mu.Unlock()
	p.srv.SetBooks(books.Bank())
	return nil
}

func (p *Participant) ready() bool {
	return !slices.ContainsFunc(p.steps, func(s server.Step) bool { return !s.Done })
}

func (p *Participant) Ensure(ctx context.Context) {
	for !p.ready() {
		p.ensureDue(ctx)
		if p.ready() || !p.pause(ctx, p.retryAt.Sub(p.clock.Real())) {
			return
		}
	}
}

func (p *Participant) provision(ctx context.Context) {
	p.Ensure(ctx)
	for attempts := 1; p.ready() && !p.promote(ctx); attempts++ {
		if !p.pause(ctx, clock.Backoff(attempts)) {
			return
		}
	}
}

func (p *Participant) pause(ctx context.Context, wait time.Duration) bool {
	timer := time.NewTimer(max(wait, time.Millisecond))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (p *Participant) ensureDue(ctx context.Context) {
	if p.ready() || p.clock.Real().Before(p.retryAt) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, ensureLimit)
	defer cancel()
	b := p.cfg.Banks[0]
	for _, e := range p.ensureSteps(b) {
		i := slices.IndexFunc(p.steps, func(s server.Step) bool { return s.ID == e.id })
		if p.steps[i].Done {
			continue
		}
		if !p.ensureStep(ctx, i, e) {
			p.srv.Provision(p.steps, runningPhase(b), fmt.Sprintf("Step %s failed. Trying again.", e.id))
			return
		}
		p.srv.Provision(p.steps, runningPhase(b), "")
	}
	p.attempts = 0
	p.srv.Provision(p.steps, runningPhase(b), "")
}

func (p *Participant) ensureStep(ctx context.Context, i int, e ensureStep) bool {
	started := p.clock.Now()
	n, err := e.run(ctx)
	st := &p.steps[i]
	st.StartedAt = &started
	if err != nil {
		st.Error, st.CompletedAt = err.Error(), nil
		p.attempts++
		p.retryAt = p.clock.Real().Add(clock.Backoff(p.attempts))
		p.log.Warn("bank step failed; retrying", "bank", p.id, "step", e.id, "attempt", p.attempts, "err", err)
		return false
	}
	done := p.clock.Now()
	st.Done, st.Error, st.CompletedAt = true, "", &done
	if e.id != config.StepDatabase {
		st.Name = stepName(e.id) + " (" + config.MigrationCount(n) + ")"
	}
	return true
}

func (p *Participant) provisioningHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/v1/", p.srv.API())
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		http.Error(w, p.srv.Bank().Name+" is setting up its database. Try again in a minute.", http.StatusServiceUnavailable)
	})
	return mux
}
