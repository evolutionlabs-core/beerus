package connect

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

type stallWriter struct {
	header  http.Header
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *stallWriter) Header() http.Header { return w.header }
func (w *stallWriter) WriteHeader(int)     {}
func (w *stallWriter) Write(b []byte) (int, error) {
	w.once.Do(func() { close(w.reached) })
	<-w.release
	return len(b), nil
}

func newTestApp(t *testing.T) *App {
	t.Helper()
	cfg := initTestConfig(t, clock.DateOf(time.Now()))
	a, err := New(cfg, Options{WorldOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestResponsesAreWrittenWithoutTheLock(t *testing.T) {
	a := newTestApp(t)
	w := a.world
	from, to := w.AccountsAt("adaeze", "bank-a")[0], w.AccountsAt("ngozi", "bank-b")[0]
	transfer := fmt.Sprintf(`{"from":%q,"to":%q,"amount":"10"}`, from.Key, to.Key)
	enquiry := "/?bank_code=999002&account_number=" + to.NUBAN
	for name, c := range map[string]struct {
		handler http.HandlerFunc
		method  string
		target  string
		body    string
	}{
		"positions":    {a.ctlNIPPositions, "GET", "/", ""},
		"participants": {a.ctlNIPParticipants, "GET", "/", ""},
		"enquiry":      {a.ctlNameEnquiry, "GET", enquiry, ""},
		"status":       {a.ctlNIPStatus, "GET", "/", ""},
		"transfer":     {a.ctlTransfer, "POST", "/", transfer},
		"replay":       {a.ctlTransfer, "POST", "/", transfer},
	} {
		sw := &stallWriter{header: http.Header{}, reached: make(chan struct{}), release: make(chan struct{})}
		r := httptest.NewRequest(c.method, c.target, strings.NewReader(c.body))
		r.Header.Set("Idempotency-Key", "lock-test")
		done := make(chan struct{})
		go func() {
			c.handler(sw, r)
			close(done)
		}()
		select {
		case <-sw.reached:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s never wrote", name)
		}
		if !a.mu.TryLock() {
			t.Fatalf("%s writes its response while holding the lock", name)
		}
		a.mu.Unlock()
		close(sw.release)
		<-done
	}
}

func TestOldNIPRecordsAreDropped(t *testing.T) {
	a := newTestApp(t)
	now := time.Now()
	a.st.NIP.Failures["old"] = &NIPFailure{SessionID: "old", CreatedReal: now.Add(-nipRetention - time.Minute)}
	a.st.NIP.Failures["recent"] = &NIPFailure{SessionID: "recent", CreatedReal: now.Add(-nipRetention + time.Minute)}
	a.pruneNIPLocked(now)
	if a.st.NIP.Failures["old"] != nil || a.st.NIP.Failures["recent"] == nil {
		t.Fatalf("failures after pruning: %v", a.st.NIP.Failures)
	}
}
