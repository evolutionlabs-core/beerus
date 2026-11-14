package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	IdempotencyTTL    = 24 * time.Hour
	idempotencyKeyMax = 255
	ControlOwner      = "control"
)

type Record struct {
	Hash        string    `json:"hash"`
	Status      int       `json:"status"`
	ContentType string    `json:"content_type"`
	Body        []byte    `json:"body"`
	CreatedReal time.Time `json:"created_real"`
}

type Store interface {
	Lookup(scope string) *Record
	Commit(scope string, rec *Record) error
}

type Hold struct {
	mu   sync.Mutex
	refs int
}

type Idempotency struct {
	mu    sync.Mutex
	locks map[string]*Hold
}

func (i *Idempotency) Acquire(scope string) *Hold {
	i.mu.Lock()
	if i.locks == nil {
		i.locks = map[string]*Hold{}
	}
	e := i.locks[scope]
	if e == nil {
		e = &Hold{}
		i.locks[scope] = e
	}
	e.refs++
	i.mu.Unlock()
	e.mu.Lock()
	return e
}

func (i *Idempotency) Release(scope string, e *Hold) {
	e.mu.Unlock()
	i.mu.Lock()
	e.refs--
	if e.refs == 0 {
		delete(i.locks, scope)
	}
	i.mu.Unlock()
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

func (i *Idempotency) Wrap(store Store, owner func(*http.Request) string, log *slog.Logger, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" || len(key) > idempotencyKeyMax {
			WriteError(w, r, http.StatusBadRequest, "invalid_request", "An Idempotency-Key header of 1 to 255 characters is required on writes.")
			return
		}
		body, err := readBody(r)
		if errors.Is(err, ErrBodyTooLarge) {
			WriteError(w, r, http.StatusRequestEntityTooLarge, "invalid_request", "Bodies over 1 MiB are refused.")
			return
		}
		if err != nil {
			WriteError(w, r, http.StatusBadRequest, "invalid_request", "The request body could not be read.")
			return
		}
		scope := strings.Join([]string{owner(r), r.Method, r.URL.Path, key}, " ")
		sum := sha256.Sum256(append([]byte(r.Method+" "+r.URL.RequestURI()+"\n"), body...))
		hash := hex.EncodeToString(sum[:])
		entry := i.Acquire(scope)
		defer i.Release(scope, entry)
		if rec := store.Lookup(scope); rec != nil {
			if rec.Hash != hash {
				WriteError(w, r, http.StatusUnprocessableEntity, "idempotency_key_reused", "This Idempotency-Key was used with a different request.")
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
		var rec *Record
		if rw.status < 500 && rw.status != http.StatusTooManyRequests {
			rec = &Record{Hash: hash, Status: rw.status, ContentType: rw.Header().Get("Content-Type"), Body: rw.buf.Bytes()}
		}
		if err := store.Commit(scope, rec); err != nil {
			log.Error("save state", "err", err)
			WriteError(w, r, http.StatusInternalServerError, "internal_error", "The change could not be saved.")
			return
		}
		w.WriteHeader(rw.status)
		_, _ = w.Write(rw.buf.Bytes())
	}
}
