package connect

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/pki"
)

type ctxKey int

const (
	ctxPartner ctxKey = iota
	ctxRequestID
)

func requestID(r *http.Request) string {
	if v, ok := r.Context().Value(ctxRequestID).(string); ok {
		return v
	}
	return ""
}

func partnerFrom(r *http.Request) *config.Partner {
	p, _ := r.Context().Value(ctxPartner).(*config.Partner)
	return p
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

type apiError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	writeJSON(w, status, map[string]apiError{"error": {Code: code, Message: msg, RequestID: requestID(r)}})
}

func (a *App) withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newID("req")
		w.Header().Set("Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxRequestID, id)))
	})
}

func keyMatches(key, hash string) bool {
	got := config.HashKey(key)
	return subtle.ConstantTimeCompare([]byte(got), []byte(hash)) == 1
}

func (a *App) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
			writeError(w, r, http.StatusUnauthorized, "unauthenticated", "A client certificate from the Connect CA is required.")
			return
		}
		cert := r.TLS.PeerCertificates[0]
		var p *config.Partner
		for _, x := range a.partnersSnapshot() {
			if x.CertCN == cert.Subject.CommonName {
				p = x
			}
		}
		role := ""
		if len(cert.Subject.OrganizationalUnit) > 0 {
			role = cert.Subject.OrganizationalUnit[0]
		}
		if p == nil || role != "partner" || (p.CertSHA256 != "" && subtle.ConstantTimeCompare([]byte(pki.Thumbprint(cert)), []byte(p.CertSHA256)) != 1) {
			writeError(w, r, http.StatusForbidden, "forbidden", "The client certificate does not belong to a registered partner.")
			return
		}
		key := r.Header.Get("X-Api-Key")
		if key == "" {
			writeError(w, r, http.StatusUnauthorized, "unauthenticated", "The X-Api-Key header is required.")
			return
		}
		if !keyMatches(key, p.APIKeySHA256) {
			for _, other := range a.partnersSnapshot() {
				if other != p && keyMatches(key, other.APIKeySHA256) {
					writeError(w, r, http.StatusForbidden, "forbidden", "The API key does not belong to the client certificate's partner.")
					return
				}
			}
			writeError(w, r, http.StatusUnauthorized, "unauthenticated", "The API key is not valid.")
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
			writeError(w, r, http.StatusTooManyRequests, "rate_limited", "Too many requests.")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxPartner, p)))
	})
}

type recorder struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (rec *recorder) WriteHeader(s int) {
	if rec.status == 0 {
		rec.status = s
	}
}

func (rec *recorder) Write(b []byte) (int, error) {
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	return rec.buf.Write(b)
}

const idemTTL = 24 * time.Hour

func (a *App) idempotent(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" || len(key) > 255 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "An Idempotency-Key header of 1 to 255 characters is required on writes.")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "The request body could not be read.")
			return
		}
		p := partnerFrom(r)
		scope := strings.Join([]string{p.ID, r.Method, r.URL.Path, key}, " ")
		sum := sha256.Sum256(append([]byte(r.Method+" "+r.URL.RequestURI()+"\n"), body...))
		hash := hex.EncodeToString(sum[:])
		lockAny, _ := a.idemLock.LoadOrStore(scope, &sync.Mutex{})
		lock := lockAny.(*sync.Mutex)
		lock.Lock()
		defer lock.Unlock()
		a.mu.Lock()
		rec := a.st.Idem[scope]
		if rec != nil && a.clock.Real().Sub(rec.CreatedReal) > idemTTL {
			delete(a.st.Idem, scope)
			rec = nil
		}
		a.mu.Unlock()
		if rec != nil {
			if rec.Hash != hash {
				writeError(w, r, http.StatusUnprocessableEntity, "idempotency_key_reused", "This Idempotency-Key was used with a different request.")
				return
			}
			if rec.ContentType != "" {
				w.Header().Set("Content-Type", rec.ContentType)
			}
			w.Header().Set("Idempotent-Replayed", "true")
			w.WriteHeader(rec.Status)
			_, _ = w.Write(rec.Body)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		rw := &recorder{ResponseWriter: w}
		next(rw, r)
		if rw.status == 0 {
			rw.status = http.StatusOK
		}
		a.mu.Lock()
		if rw.status < 500 && rw.status != http.StatusTooManyRequests {
			a.st.Idem[scope] = &IdemRec{Hash: hash, Status: rw.status, ContentType: rw.Header().Get("Content-Type"), Body: rw.buf.Bytes(), CreatedReal: a.clock.Real()}
		}
		err = a.saveLocked()
		a.mu.Unlock()
		if err != nil {
			a.log.Error("save state", "err", err)
			writeError(w, r, http.StatusInternalServerError, "internal_error", "The change could not be saved.")
			return
		}
		w.WriteHeader(rw.status)
		_, _ = w.Write(rw.buf.Bytes())
	}
}

func decodeBody(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
