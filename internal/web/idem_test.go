package web

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestIdempotencyLocksExcludeAndAreReleased(t *testing.T) {
	var a Idempotency
	var wg sync.WaitGroup
	inside := map[string]int{}
	var mu sync.Mutex
	for _, scope := range []string{"s1", "s1", "s1", "s2", "s2"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e := a.Acquire(scope)
			mu.Lock()
			inside[scope]++
			if inside[scope] != 1 {
				t.Errorf("two requests held %s at once", scope)
			}
			mu.Unlock()
			mu.Lock()
			inside[scope]--
			mu.Unlock()
			a.Release(scope, e)
		}()
	}
	wg.Wait()
	if n := len(a.locks); n != 0 {
		t.Fatalf("%d locks left after every request finished", n)
	}
	e := a.Acquire("s3")
	if len(a.locks) != 1 {
		t.Fatalf("held lock missing: %d", len(a.locks))
	}
	a.Release("s3", e)
	if len(a.locks) != 0 {
		t.Fatal("lock not removed on release")
	}
}

type capture struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (c *capture) Header() http.Header {
	return c.header
}

func (c *capture) WriteHeader(status int) {
	c.status = status
}

func (c *capture) Write(b []byte) (int, error) {
	return c.body.Write(b)
}

type memoryStore struct {
	mu   sync.Mutex
	recs map[string]*Record
}

func (m *memoryStore) Lookup(scope string) *Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.recs[scope]
}

func (m *memoryStore) Commit(scope string, rec *Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rec != nil {
		m.recs[scope] = rec
	}
	return nil
}

func TestIdempotentWrites(t *testing.T) {
	store := &memoryStore{recs: map[string]*Record{}}
	var calls int
	var i Idempotency
	h := WithRequestID(i.Wrap(store, func(*http.Request) string { return ControlOwner }, slog.New(slog.NewTextHandler(io.Discard, nil)), func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Query().Get("answer") {
		case "500":
			WriteJSON(w, http.StatusInternalServerError, map[string]string{"n": "x"})
		case "429":
			WriteJSON(w, http.StatusTooManyRequests, map[string]string{"n": "x"})
		default:
			WriteJSON(w, http.StatusCreated, map[string]string{"n": "x"})
		}
	}))
	rows := []struct {
		name     string
		path     string
		key      string
		body     string
		status   int
		replayed bool
		calls    int
		message  string
	}{
		{"first write", "/w", "k1", `{"a":1}`, 201, false, 1, ""},
		{"replay", "/w", "k1", `{"a":1}`, 201, true, 1, ""},
		{"key reused with another body", "/w", "k1", `{"a":2}`, 422, false, 1, "This Idempotency-Key was used with a different request."},
		{"no key", "/w", "", `{}`, 400, false, 1, "An Idempotency-Key header of 1 to 255 characters is required on writes."},
		{"server error is not recorded", "/w?answer=500", "k2", `{}`, 500, false, 2, ""},
		{"server error runs again", "/w?answer=500", "k2", `{}`, 500, false, 3, ""},
		{"rate limit is not recorded", "/w?answer=429", "k3", `{}`, 429, false, 4, ""},
		{"rate limit runs again", "/w?answer=429", "k3", `{}`, 429, false, 5, ""},
		{"body over the limit", "/w", "k4", strings.Repeat("a", BodyLimit+1), 413, false, 5, "Bodies over 1 MiB are refused."},
	}
	for _, r := range rows {
		req, err := http.NewRequest(http.MethodPost, r.path, strings.NewReader(r.body))
		if err != nil {
			t.Fatal(err)
		}
		if r.key != "" {
			req.Header.Set("Idempotency-Key", r.key)
		}
		rec := &capture{header: http.Header{}}
		h.ServeHTTP(rec, req)
		var out struct {
			Error struct{ Message string }
		}
		_ = json.Unmarshal(rec.body.Bytes(), &out)
		if rec.status != r.status || (rec.header.Get("Idempotent-Replayed") == "true") != r.replayed || calls != r.calls || out.Error.Message != r.message {
			t.Errorf("%s: %d replayed %q, %d calls, message %q; want %d, %v, %d, %q", r.name, rec.status, rec.header.Get("Idempotent-Replayed"), calls, out.Error.Message, r.status, r.replayed, r.calls, r.message)
		}
	}
}
