package core

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/control"
)

const lockWait = 10 * time.Second

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

func nipPeers(r *http.Request) (*http.Response, error) {
	switch p := r.URL.Path; {
	case strings.HasSuffix(p, nibssOpenSuffix):
		return stubReply(http.StatusOK, `{"transfers":0,"outward_minor":0,"inward_minor":0,"unposted_settlements":0}`), nil
	case p == nibssStatusPath:
		return stubReply(http.StatusOK, `{"next_cutoff_at":"2026-11-04T11:00:00Z"}`), nil
	case p == control.NIBSSDirectoryPath:
		return stubReply(http.StatusOK, `{"data":[]}`), nil
	case p == nibssEnquiriesPath:
		return stubReply(http.StatusOK, `{"response_code":"00","account_name":"Ngozi Eze"}`), nil
	}
	return stubReply(http.StatusOK, `{"status":"success"}`), nil
}

func TestResponsesAreWrittenWithoutTheLock(t *testing.T) {
	link := control.Link{Token: "token", Client: &http.Client{Transport: roundTrip(nipPeers)}}
	a := &App{
		cfg:      bankDirConfig(t, &config.Bank{ID: "bank-a", Port: 1}),
		link:     link,
		nibssCtl: &nibssAdmin{control.NewNIBSSControl(link, 1)},
		st:       newState(),
		log:      slog.New(slog.DiscardHandler),
		clock:    clock.New(),
		status:   map[string]*BankStatus{"bank-a": {ID: "bank-a", Phase: PhaseActive}},
	}
	transfer := `{"from":"0000000014","to_bank":"bank-b","to":"0000000017","amount":"10"}`
	for name, c := range map[string]struct {
		handler http.HandlerFunc
		target  string
		body    string
	}{
		"transfer":     {a.atBank, "/banks/bank-a/transfers", transfer},
		"positions":    {a.ctlNIPPositions, "/", ""},
		"participants": {a.ctlNIPParticipants, "/", ""},
		"enquiry":      {a.ctlNameEnquiry, "/?bank_code=999002&account_number=0000000017", ""},
	} {
		sw := &stallWriter{header: http.Header{}, reached: make(chan struct{}), release: make(chan struct{})}
		r := httptest.NewRequest(http.MethodPost, c.target, strings.NewReader(c.body))
		r.SetPathValue("id", "bank-a")
		r.Header.Set("Idempotency-Key", "lock-test")
		done := make(chan struct{})
		go func() {
			c.handler(sw, r)
			close(done)
		}()
		deadline := time.NewTimer(lockWait)
		select {
		case <-sw.reached:
		case <-deadline.C:
			t.Fatalf("%s never wrote", name)
		}
		for !a.mu.TryLock() {
			select {
			case <-deadline.C:
				t.Fatalf("%s writes its response while holding the lock", name)
			case <-time.After(time.Millisecond):
			}
		}
		deadline.Stop()
		a.mu.Unlock()
		close(sw.release)
		<-done
	}
}
