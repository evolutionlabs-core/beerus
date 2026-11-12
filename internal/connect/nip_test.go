package connect

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
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

func TestResponsesAreWrittenWithoutTheLock(t *testing.T) {
	a := newTestApp(t, Options{})
	from := funded(t, a, "bank-a", "Ada Obi", "50000")
	to := funded(t, a, "bank-b", "Ngozi Eze", "")
	transfer := `{"from":"` + from + `","to_bank":"bank-b","to":"` + to + `","amount":"10"}`
	for name, c := range map[string]struct {
		handler http.HandlerFunc
		target  string
		body    string
	}{
		"transfer":     {a.ctlBankTransfer, "/", transfer},
		"replay":       {a.ctlBankTransfer, "/", transfer},
		"positions":    {a.ctlNIPPositions, "/", ""},
		"participants": {a.ctlNIPParticipants, "/", ""},
		"enquiry":      {a.ctlNameEnquiry, "/?bank_code=999002&account_number=0000000017", ""},
		"settlement":   {a.ctlNIPSettlement, "/", ""},
	} {
		sw := &stallWriter{header: http.Header{}, reached: make(chan struct{}), release: make(chan struct{})}
		r := httptest.NewRequest("POST", c.target, strings.NewReader(c.body))
		r.SetPathValue("id", "bank-a")
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
