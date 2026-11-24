package aggregator

import (
	"context"
	"crypto/subtle"
	"net/http"

	"golang.org/x/time/rate"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

type ctxKey int

const (
	ctxPartner ctxKey = iota
)

func partnerFrom(r *http.Request) *config.Partner {
	p, _ := r.Context().Value(ctxPartner).(*config.Partner)
	return p
}

func idemOwner(r *http.Request) string {
	if p := partnerFrom(r); p != nil {
		return p.ID
	}
	return web.ControlOwner
}

func keyMatches(key, hash string) bool {
	got := config.HashKey(key)
	return subtle.ConstantTimeCompare([]byte(got), []byte(hash)) == 1
}

func (a *App) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-Api-Key")
		if key == "" {
			web.WriteError(w, r, http.StatusUnauthorized, "unauthenticated", "The X-Api-Key header is required.")
			return
		}
		var p *config.Partner
		for _, x := range a.partnersSnapshot() {
			if keyMatches(key, x.APIKeySHA256) {
				p = x
			}
		}
		if p == nil {
			web.WriteError(w, r, http.StatusUnauthorized, "unauthenticated", "The API key is not valid.")
			return
		}
		a.mu.Lock()
		lim := a.limiters[p.ID]
		if lim == nil {
			lim = rate.NewLimiter(500, 1000)
			a.limiters[p.ID] = lim
		}
		a.mu.Unlock()
		if !lim.Allow() {
			w.Header().Set("Retry-After", "1")
			web.WriteError(w, r, http.StatusTooManyRequests, "rate_limited", "Too many requests.")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxPartner, p)))
	})
}

type idemStore struct {
	a *App
}

func (s idemStore) Lookup(scope string) *web.Record {
	s.a.mu.Lock()
	defer s.a.mu.Unlock()
	return s.a.idemGetLocked(scope)
}

func (s idemStore) Commit(scope string, rec *web.Record) error {
	s.a.mu.Lock()
	defer s.a.mu.Unlock()
	if rec != nil {
		s.a.idemPutLocked(scope, rec.Hash, rec.Status, rec.ContentType, rec.Body)
	}
	if s.a.statePath == "" {
		return nil
	}
	return s.a.saveLocked()
}

func (a *App) idemGetLocked(scope string) *web.Record {
	rec := a.st.Idem[scope]
	if rec != nil && a.clock.Real().Sub(rec.CreatedReal) > web.IdempotencyTTL {
		delete(a.st.Idem, scope)
		return nil
	}
	return rec
}

func (a *App) idemPutLocked(scope, hash string, status int, contentType string, body []byte) {
	a.st.Idem[scope] = &web.Record{Hash: hash, Status: status, ContentType: contentType, Body: body, CreatedReal: a.clock.Real()}
}

func (a *App) idempotent(next http.HandlerFunc) http.HandlerFunc {
	return a.idem.Wrap(idemStore{a}, idemOwner, a.log, next)
}
