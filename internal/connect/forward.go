package connect

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

const (
	forwardTimeout = 10 * time.Second
	bankAPIPrefix  = "/v1/"
)

type badAnswerError struct {
	bank string
	err  error
}

func (e *badAnswerError) Error() string {
	return fmt.Sprintf("the answer of %s is not usable: %v", e.bank, e.err)
}

func (e *badAnswerError) Unwrap() error {
	return e.err
}

func (a *App) atBank(w http.ResponseWriter, r *http.Request) {
	a.forwardToBank(w, r, false)
}

func (a *App) atBankSyncing(w http.ResponseWriter, r *http.Request) {
	a.forwardToBank(w, r, true)
}

func (a *App) bankingError(w http.ResponseWriter, r *http.Request, err error) {
	server.WriteBankingError(w, r, a.log, a.noBanks, err)
}

func (a *App) forwardToBank(w http.ResponseWriter, r *http.Request, sync bool) {
	id := r.PathValue("id")
	b := findBank(a.bankList(), id)
	if b == nil {
		a.noSuchBank(w, r)
		return
	}
	key, ok := web.IdempotencyKey(w, r)
	if !ok {
		return
	}
	phase := a.bankPhase(id)
	if phase == PhaseClosed || b.Closed() {
		web.WriteError(w, r, http.StatusConflict, "bank_closed", fmt.Sprintf("%s is closed.", b.Name))
		return
	}
	write := r.Method != http.MethodGet
	if write {
		if phase != PhaseActive {
			a.bankingError(w, r, control.NotActiveError{Name: b.Name, Phase: phase})
			return
		}
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, web.BodyLimit+1))
	if err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "The request body could not be read.")
		return
	}
	if len(body) > web.BodyLimit {
		web.WriteError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", fmt.Sprintf("The request body is larger than %d MiB.", web.BodyLimit>>20))
		return
	}
	status, header, out, err := a.callBank(r.Context(), b, r, key, body)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		var bad *badAnswerError
		if errors.As(err, &bad) {
			a.log.Warn("bank answer refused", "bank", id, "err", err)
			web.WriteError(w, r, http.StatusBadGateway, "bad_gateway", fmt.Sprintf("%s sent an answer that could not be used: %v.", b.Name, bad.err))
			return
		}
		a.log.Warn("bank not reachable", "bank", id, "err", err)
		web.WriteError(w, r, http.StatusServiceUnavailable, "unavailable", fmt.Sprintf(bankAway, b.Name))
		return
	}
	for _, name := range []string{"Content-Type", "Idempotent-Replayed"} {
		if v := header.Get(name); v != "" {
			w.Header().Set(name, v)
		}
	}
	w.WriteHeader(status)
	if _, err := w.Write(out); err != nil {
		a.log.Warn("write forwarded answer", "bank", id, "err", err)
	}
	if sync && status < http.StatusMultipleChoices {
		a.SyncAll(r.Context())
	}
}

func (a *App) callBank(ctx context.Context, b *config.Bank, r *http.Request, key string, body []byte) (int, http.Header, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, forwardTimeout)
	defer cancel()
	_, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.EscapedPath(), "/banks/"), "/")
	target := b.URL() + bankAPIPrefix + rest
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, target, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.link.Token)
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := a.link.Client.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("call %s: %w", b.ID, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, control.MaxPeerBody+1))
	if err != nil {
		return 0, nil, nil, &badAnswerError{bank: b.ID, err: fmt.Errorf("read failed: %w", err)}
	}
	if len(out) > control.MaxPeerBody {
		return 0, nil, nil, &badAnswerError{bank: b.ID, err: fmt.Errorf("answer is larger than %d MiB", control.MaxPeerBody>>20)}
	}
	return resp.StatusCode, resp.Header, out, nil
}
