package contracttest

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/aggregator"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHungBankDoesNotStallPartnerAPI(t *testing.T) {
	if testingExternal() {
		t.Skip("needs the in-process stack")
	}
	var hang atomic.Bool
	var held atomic.Int32
	release := make(chan struct{})
	opts := aggregator.Options{BankWrap: func(base http.RoundTripper) http.RoundTripper {
		return roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if hang.Load() {
				held.Add(1)
				select {
				case <-release:
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
			}
			return base.RoundTrip(r)
		})
	}}
	s := startStackWith(t, opts)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	conn := s.connect("ngozi", "bank-b")
	id := fmt.Sprint(conn.Accounts[0]["id"])

	hang.Store(true)
	ref := s.must(s.call("POST", "/v1/accounts/"+id+"/refresh", nil, withToken(conn.AccessToken)), 202)
	deadline := time.Now().Add(5 * time.Second)
	for held.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if held.Load() == 0 {
		t.Fatal("the sync never reached the bank")
	}

	fast := func(name string, f func()) {
		t.Helper()
		start := time.Now()
		f()
		if d := time.Since(start); d > time.Second {
			t.Errorf("%s took %s while a bank was hung", name, d)
		}
	}
	fast("get connection", func() { s.must(s.call("GET", "/v1/connection", nil, withToken(conn.AccessToken)), 200) })
	fast("get account", func() { s.must(s.call("GET", "/v1/accounts/"+id, nil, withToken(conn.AccessToken)), 200) })
	fast("sync feed", func() {
		s.must(s.call("GET", "/v1/accounts/"+id+"/transactions/sync", nil, withToken(conn.AccessToken)), 200)
	})
	fast("new link token", func() {
		s.linkURL("w", "")
	})

	hang.Store(false)
	close(release)
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if len(s.eventsOf("refresh.completed", func(d map[string]any) bool { return d["refresh_id"] == ref.JSON["id"] })) > 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the refresh never finished after the bank answered")
}
