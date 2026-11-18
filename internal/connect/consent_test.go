package connect

import (
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/consent"
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
	a.st.Revokes[0].Next = a.clock.Real()
	started := a.clock.Real()
	a.retryRevokes()
	finished := a.clock.Real()
	if len(a.st.Revokes) != 1 || a.st.Revokes[0].Attempts != 2 {
		t.Fatalf("queue after a second failure: %+v", a.st.Revokes)
	}
	if next := a.st.Revokes[0].Next; next.Before(started.Add(consent.Backoff(2))) || next.After(finished.Add(consent.Backoff(2))) {
		t.Fatalf("second retry scheduled for %v, want %v after the attempt", next, consent.Backoff(2))
	}
	up = true
	a.st.Revokes[0].Next = a.clock.Real()
	a.retryRevokes()
	if len(a.st.Revokes) != 0 {
		t.Fatalf("queue after the bank came back: %+v", a.st.Revokes)
	}
}

func TestTokenWindows(t *testing.T) {
	at := time.Date(2026, time.November, 14, 10, 0, 0, 0, clock.Lagos)
	for _, c := range []struct {
		name    string
		expires time.Time
		usable  bool
		due     bool
	}{
		{"expired an hour ago", at.Add(-time.Hour), false, true},
		{"expires now", at, false, true},
		{"inside the margin", at.Add(refreshMargin), false, true},
		{"a second past the margin", at.Add(refreshMargin + time.Second), true, true},
		{"inside the look-ahead", at.Add(refreshAhead), true, true},
		{"a second past the look-ahead", at.Add(refreshAhead + time.Second), true, false},
		{"far future", time.Date(2126, time.March, 1, 0, 0, 0, 0, clock.Lagos), true, false},
	} {
		if got := accessUsable(c.expires, at); got != c.usable {
			t.Errorf("%s: usable %v, want %v", c.name, got, c.usable)
		}
		if got := refreshDue(c.expires, at); got != c.due {
			t.Errorf("%s: refresh due %v, want %v", c.name, got, c.due)
		}
	}
}

func TestRevokeDue(t *testing.T) {
	next := time.Date(2026, time.November, 14, 10, 0, 0, 0, clock.Lagos)
	for _, c := range []struct {
		name string
		now  time.Time
		want bool
	}{
		{"a second before", next.Add(-time.Second), false},
		{"at the instant", next, true},
		{"after", next.Add(time.Hour), true},
	} {
		if got := revokeDue(next, c.now); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
