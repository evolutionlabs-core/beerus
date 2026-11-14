package connect

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/evolutionlabs-core/beerus/internal/config"
)

const (
	forwardBodyMax = 1 << 20
	bankAPIPrefix  = "/v1/"
)

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
	write := r.Method != http.MethodGet
	if write {
		if phase := a.bankPhase(id); phase != PhaseActive {
			a.bankingError(w, r, notActiveError{b.Name, phase})
			return
		}
		if key == "" {
			key = newID("idk")
		}
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, forwardBodyMax+1))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body could not be read.")
		return
	}
	if len(body) > forwardBodyMax {
		writeError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", fmt.Sprintf("The request body is larger than %d MiB.", forwardBodyMax>>20))
		return
	}
	status, header, out, err := a.callBank(r.Context(), b, r, key, body)
	if err != nil {
		if r.Context().Err() != nil {
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
	_, _ = w.Write(out)
	if sync && status < http.StatusMultipleChoices {
		a.SyncAll(r.Context())
	}
}

func (a *App) callBank(ctx context.Context, b *config.Bank, r *http.Request, key string, body []byte) (int, http.Header, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, peerTimeout)
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
		return 0, nil, nil, fmt.Errorf("read the answer of %s: %w", b.ID, err)
	}
	if len(out) > maxPeerBody {
		return 0, nil, nil, fmt.Errorf("the answer of %s is larger than %d MiB", b.ID, maxPeerBody>>20)
	}
	return resp.StatusCode, resp.Header, out, nil
}
