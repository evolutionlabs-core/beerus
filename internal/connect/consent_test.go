package connect

import (
	"io"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

func TestConsentLedgerAndSaveDoNotRace(t *testing.T) {
	a := &App{st: newState()}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			consentLedger{a}.Record(openbanking.ConsentEvent{ConsentID: "obc_1", Status: openbanking.Revoked})
		}()
		go func() {
			defer wg.Done()
			a.mu.Lock()
			a.dirty()
			a.isDirty.Load()
			a.isDirty.Store(false)
			a.mu.Unlock()
		}()
	}
	wg.Wait()
	if got := a.ConsentLedger("", 100); len(got) != 8 {
		t.Fatalf("ledger has %d entries", len(got))
	}
}

func TestEventBackoffGrowsAndStops(t *testing.T) {
	last := time.Duration(0)
	for n := 0; n < 40; n++ {
		d := eventDelay(n)
		if d < last || d > eventBackoffAt {
			t.Fatalf("attempt %d waits %s after %s", n, d, last)
		}
		last = d
	}
	if eventDelay(0) != eventBackoff || last != eventBackoffAt {
		t.Fatalf("backoff runs from %s to %s", eventDelay(0), last)
	}
}

func TestEventIDsAreTrackedOnceAndBounded(t *testing.T) {
	var j jtiSet
	now := time.Now()
	if ok, err := j.first("bank-a|1", now.Add(time.Minute), now); !ok || err != nil {
		t.Fatalf("first sight: %v %v", ok, err)
	}
	if ok, _ := j.first("bank-a|1", now.Add(time.Minute), now); ok {
		t.Fatal("a repeated event id was accepted")
	}
	for i := 0; i < seenLimit; i++ {
		j.first("id-"+time.Duration(i).String(), now.Add(time.Hour), now)
	}
	if _, err := j.first("one-more", now.Add(time.Hour), now); err != errTooManyEvents {
		t.Fatalf("unbounded growth: %v", err)
	}
	later := now.Add(2 * time.Hour)
	if ok, err := j.first("after-expiry", later.Add(time.Minute), later); !ok || err != nil {
		t.Fatalf("expired ids were not dropped: %v %v", ok, err)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestFailedRevokesAreQueuedAndRetried(t *testing.T) {
	now := time.Now()
	up := false
	calls := 0
	a := &App{
		cfg:   &config.Config{Banks: []*config.Bank{{ID: "bank-a", Port: 7001, APIPort: 7011}}},
		st:    newState(),
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		clock: clock.New(clock.State{Pace: 1}, func() time.Time { return now }),
		obHTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			calls++
			code := http.StatusServiceUnavailable
			if up {
				code = http.StatusNoContent
			}
			return &http.Response{StatusCode: code, Body: http.NoBody, Header: http.Header{}}, nil
		})},
	}
	b := a.cfg.Bank("bank-a")
	a.mu.Lock()
	defer a.mu.Unlock()
	a.revokeOrQueue(b, "obc_1")
	if len(a.st.Revokes) != 1 || a.st.Revokes[0].Attempts != 1 {
		t.Fatalf("queue after a failed revoke: %+v", a.st.Revokes)
	}
	before := calls
	a.retryRevokes()
	if calls != before || len(a.st.Revokes) != 1 {
		t.Fatalf("retried before it was due: %d calls, %+v", calls-before, a.st.Revokes)
	}
	now = now.Add(time.Hour)
	a.retryRevokes()
	if len(a.st.Revokes) != 1 || a.st.Revokes[0].Attempts != 2 {
		t.Fatalf("queue after a second failure: %+v", a.st.Revokes)
	}
	up = true
	now = now.Add(time.Hour)
	a.retryRevokes()
	if len(a.st.Revokes) != 0 {
		t.Fatalf("queue after the bank came back: %+v", a.st.Revokes)
	}
}
