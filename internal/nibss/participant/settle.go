package participant

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

const settlePoll = 5 * time.Second

type settleRequest struct {
	adhoc bool
	reply chan settleReply
}

type settleReply struct {
	posted int
	err    error
}

type settleBody struct {
	Adhoc *bool `json:"adhoc"`
}

func (p *Participant) runSettlement(ctx context.Context) {
	attempts := 0
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-p.settles:
			n, err := p.settlePass(ctx, req.adhoc)
			req.reply <- settleReply{posted: n, err: err}
		case <-timer.C:
			_, err := p.settlePass(ctx, false)
			delay := settlePoll
			if err != nil && ctx.Err() == nil {
				attempts++
				delay = clock.Backoff(attempts)
				p.log.WarnContext(ctx, "settlement not posted; retrying", "err", err, "attempts", attempts)
			} else {
				attempts = 0
			}
			timer.Reset(delay)
		}
	}
}

func (p *Participant) settleNow(ctx context.Context, adhoc bool) (int, error) {
	req := settleRequest{adhoc: adhoc, reply: make(chan settleReply, 1)}
	select {
	case p.settles <- req:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	select {
	case out := <-req.reply:
		return out.posted, out.err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (p *Participant) settlePass(ctx context.Context, adhoc bool) (int, error) {
	f, err := config.LoadNIBSSFile(p.dir)
	if err != nil {
		return 0, err
	}
	cal, err := f.Calendar()
	if err != nil {
		return 0, err
	}
	todo, err := p.store.Settle(ctx, nibss.Schedule{Per: f.Sessions, Cal: cal}, p.clock.Now(), adhoc)
	if err != nil {
		return 0, fmt.Errorf("settle sessions: %w", err)
	}
	if len(todo) == 0 {
		return 0, nil
	}
	directory, err := p.store.Directory(ctx)
	if err != nil {
		return 0, fmt.Errorf("read the directory: %w", err)
	}
	banks := make(map[string]string, len(directory))
	for _, in := range directory {
		banks[in.NIPCode] = in.ID
	}
	for _, x := range todo {
		nets, err := x.Nets(banks)
		if err != nil {
			return 0, err
		}
		if moved := nibss.Moved(nets); len(moved) >= 2 {
			if err := p.cbn.post(ctx, x.Key, x.CutoffAt, moved); err != nil {
				return 0, err
			}
		}
		if err := p.store.MarkPosted(ctx, x.Key, x.CutoffAt); err != nil {
			return 0, err
		}
	}
	return len(todo), nil
}

func (p *Participant) settlementSession(w http.ResponseWriter, r *http.Request) {
	var req settleBody
	if err := web.DecodeBody(r, &req); err != nil || req.Adhoc == nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", `Send {"adhoc":true} to settle open sessions now, or {"adhoc":false} to settle what is due.`)
		return
	}
	n, err := p.settleNow(r.Context(), *req.Adhoc)
	var refused *CBNError
	switch {
	case err == nil:
		web.WriteJSON(w, http.StatusOK, map[string]int{"posted_sessions": n})
	case errors.As(err, &refused) && !refused.Retryable():
		p.log.ErrorContext(r.Context(), "settlement refused by the CBN", "err", err)
		web.WriteError(w, r, http.StatusBadGateway, "cbn_refused", web.Sentence(refused.Message))
	case errors.As(err, &refused) || errors.Is(err, errCBNAway):
		p.log.ErrorContext(r.Context(), "settlement not posted", "err", err)
		web.WriteError(w, r, http.StatusServiceUnavailable, "unavailable", "The settlement could not reach the CBN. Try again soon.")
	default:
		p.log.ErrorContext(r.Context(), "settlement not run", "err", err)
		web.WriteError(w, r, http.StatusServiceUnavailable, "unavailable", "NIBSS could not run the settlement. Try again soon.")
	}
}
