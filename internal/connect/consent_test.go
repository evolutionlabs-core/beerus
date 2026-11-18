package connect

import (
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

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

func TestRefreshAheadIsShorterThanTheShortestAccessToken(t *testing.T) {
	if floor := time.Duration(config.MinAccessSeconds) * time.Second; refreshAhead >= floor {
		t.Fatalf("refreshAhead %s must be shorter than the %s minimum access token life", refreshAhead, floor)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestFailedRevokesAreQueuedAndRetried(t *testing.T) {
	up := false
	calls := 0
	a := &App{
		cfg:   &config.Config{Banks: []*config.Bank{{ID: "bank-a", Port: 7001, APIPort: 7011}}},
		st:    newState(),
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		clock: clock.New(),
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
	a.revokeOrQueue(b, "obc_1")
	if len(a.st.Revokes) != 1 || a.st.Revokes[0].Attempts != 1 {
		t.Fatalf("queue after a failed revoke: %+v", a.st.Revokes)
	}
	before := calls
	a.retryRevokes()
	if calls != before || len(a.st.Revokes) != 1 {
		t.Fatalf("retried before it was due: %d calls, %+v", calls-before, a.st.Revokes)
	}
	a.st.Revokes[0].Next = a.clock.Real().Add(time.Second)
	a.retryRevokes()
	if calls != before || len(a.st.Revokes) != 1 || a.st.Revokes[0].Attempts != 1 {
		t.Fatalf("retried a second before it was due: %d calls, %+v", calls-before, a.st.Revokes)
	}
	a.st.Revokes[0].Next = a.clock.Real()
	started := a.clock.Real()
	a.retryRevokes()
	finished := a.clock.Real()
	if len(a.st.Revokes) != 1 || a.st.Revokes[0].Attempts != 2 {
		t.Fatalf("queue after a second failure: %+v", a.st.Revokes)
	}
	if next := a.st.Revokes[0].Next; next.Before(started.Add(openbanking.Backoff(2))) || next.After(finished.Add(openbanking.Backoff(2))) {
		t.Fatalf("second retry scheduled for %v, want %v after the attempt", next, openbanking.Backoff(2))
	}
	up = true
	a.st.Revokes[0].Next = a.clock.Real()
	a.retryRevokes()
	if len(a.st.Revokes) != 0 {
		t.Fatalf("queue after the bank came back: %+v", a.st.Revokes)
	}
}
