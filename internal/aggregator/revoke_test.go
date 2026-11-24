package aggregator

import (
	"net/http"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

func TestFailedRevokesAreQueuedAndRetried(t *testing.T) {
	up := false
	calls := 0
	a := newApp(t, func(*http.Request) (*http.Response, error) {
		calls++
		if up {
			return bankReply(http.StatusNoContent, ""), nil
		}
		return bankReply(http.StatusServiceUnavailable, ""), nil
	})
	a.revokeOrQueue(a.cfg.Bank("bank-a"), "obc_1")
	if len(a.st.Revokes) != 1 || a.st.Revokes[0].Attempts != 1 {
		t.Fatalf("queue after a failed revoke: %+v", a.st.Revokes)
	}
	before := calls
	a.retryRevokes()
	a.st.Revokes[0].Next = a.clock.Real().Add(time.Second)
	a.retryRevokes()
	if calls != before || len(a.st.Revokes) != 1 || a.st.Revokes[0].Attempts != 1 {
		t.Fatalf("retried before it was due: %d calls, %+v", calls-before, a.st.Revokes)
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
