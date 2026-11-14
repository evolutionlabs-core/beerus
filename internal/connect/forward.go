package connect

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
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

func (a *App) atBank(local http.HandlerFunc) http.HandlerFunc {
	return a.bankRoute(local, false)
}

func (a *App) atBankSyncing(local http.HandlerFunc) http.HandlerFunc {
	return a.bankRoute(local, true)
}

func (a *App) bankRoute(local http.HandlerFunc, sync bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.role != RoleCore {
			local(w, r)
			return
		}
		a.forwardToBank(w, r, sync)
	}
}

func (a *App) forwardToBank(w http.ResponseWriter, r *http.Request, sync bool) {
	id := r.PathValue("id")
	b := findBank(a.bankList(), id)
	if b == nil {
		a.noSuchBank(w, r)
		return
	}
	key, ok := idemKey(w, r)
	if !ok {
		return
	}
	phase := a.bankPhase(id)
	if phase == PhaseClosed || b.Closed() {
		writeError(w, r, http.StatusConflict, "bank_closed", fmt.Sprintf("%s is closed.", b.Name))
		return
	}
	write := r.Method != http.MethodGet
	if write {
		if phase != PhaseActive {
			a.bankingError(w, r, notActiveError{b.Name, phase})
			return
		}
		if key == "" {
			key = newID("idk")
		}
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, bodyLimit+1))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body could not be read.")
		return
	}
	if len(body) > bodyLimit {
		writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", fmt.Sprintf("The request body is larger than %d MiB.", bodyLimit>>20))
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
			writeError(w, r, http.StatusBadGateway, "bad_gateway", fmt.Sprintf("%s sent an answer that could not be used: %v.", b.Name, bad.err))
			return
		}
		a.log.Warn("bank not reachable", "bank", id, "err", err)
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", fmt.Sprintf("%s is not running or cannot be reached. Run beerus get banks to see its state, then try again.", b.Name))
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
	req.Header.Set("Authorization", "Bearer "+a.ctlToken)
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := a.peers.Do(req)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("call %s: %w", b.ID, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, maxPeerBody+1))
	if err != nil {
		return 0, nil, nil, &badAnswerError{bank: b.ID, err: fmt.Errorf("read failed: %w", err)}
	}
	if len(out) > maxPeerBody {
		return 0, nil, nil, &badAnswerError{bank: b.ID, err: fmt.Errorf("answer is larger than %d MiB", maxPeerBody>>20)}
	}
	return resp.StatusCode, resp.Header, out, nil
}
