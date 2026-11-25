package aggregator

import (
	"fmt"
	"net/http"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
)

func TestBankCallsDoNotHoldTheLockAndLateResultsAreDropped(t *testing.T) {
	const feed = `{"data":[{"id":"t1","sequence":1,"status":"posted","amount_minor":5000,"booked_at":"2026-01-02T10:00:00Z","value_date":"2026-01-02","description":"salary","channel":"nip","balance_after_minor":5000}],"next_cursor":null}`
	for _, tc := range []struct {
		name   string
		revoke bool
		lines  int
		ledger int64
		events map[string]int
	}{
		{"untouched while the bank answers", false, 1, 5000, map[string]int{"historical_sync.completed": 1, "sync.available": 1}},
		{"revoked while the bank answers", true, 0, 0, map[string]int{"connection.status_changed": 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGate(t)
			a := newApp(t, func(r *http.Request) (*http.Response, error) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/transactions"):
					g.hold()
					return bankReply(http.StatusOK, feed), nil
				case strings.HasSuffix(r.URL.Path, "/balances"):
					return bankReply(http.StatusOK, `{"ledger_balance":{"amount_minor":5000},"available_balance":{"amount_minor":5000}}`), nil
				}
				return bankReply(http.StatusNotFound, "{}"), nil
			})
			c := &Connection{ID: "con_1", PartnerID: "p1", Institution: "bank-a", Status: "active", ConsentID: "obc_1"}
			acct := a.seedAccount(c, "0123456789")
			a.seedToken(t, c.ID, "at", time.Hour)
			done := make(chan bool, 1)
			go func() { done <- a.syncConnection(c, a.clock.Real()) }()
			g.waitEntered(t)
			locked := make(chan struct{})
			go func() {
				a.mu.Lock()
				defer a.mu.Unlock()
				if tc.revoke {
					a.setStatus(c, "revoked", "connection_revoked")
				}
				close(locked)
			}()
			select {
			case <-locked:
			case <-time.After(wait):
				t.Fatal("the lock was held across the bank call")
			}
			g.open()
			var synced bool
			select {
			case synced = <-done:
			case <-time.After(wait):
				t.Fatal("the sync never finished")
			}
			a.mu.Lock()
			defer a.mu.Unlock()
			lines := 0
			if f := a.st.Feeds[acct.ID]; f != nil {
				lines = len(f.Lines)
			}
			events := map[string]int{}
			for _, d := range a.st.Deliveries {
				events[d.Type]++
			}
			if synced != !tc.revoke || lines != tc.lines || acct.Ledger != tc.ledger || !reflect.DeepEqual(events, tc.events) {
				t.Fatalf("synced %v with %d lines, ledger %d and events %v, want %v, %d, %d and %v", synced, lines, acct.Ledger, events, !tc.revoke, tc.lines, tc.ledger, tc.events)
			}
		})
	}
}

func TestRevokeKeepsTokensOfAConnectionRelinkedMeanwhile(t *testing.T) {
	g := newGate(t)
	a := newApp(t, func(*http.Request) (*http.Response, error) {
		g.hold()
		return bankReply(http.StatusNoContent, ""), nil
	})
	c := &Connection{ID: "con_1", PartnerID: "p1", Institution: "bank-a", Status: "active", ConsentID: "obc_1"}
	a.st.Connections[c.ID] = c
	a.seedToken(t, c.ID, "old-at", time.Hour)
	done := make(chan error, 1)
	go func() { done <- a.revokeConnectionAtBank(c.ID) }()
	g.waitEntered(t)
	a.mu.Lock()
	c.ConsentID = "obc_2"
	a.seedToken(t, c.ID, "new-at", time.Hour)
	a.mu.Unlock()
	g.open()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("revoke: %v", err)
		}
	case <-time.After(wait):
		t.Fatal("the revoke never finished")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if got, err := a.openAccessLocked(c.ID); err != nil || got != "new-at" {
		t.Fatalf("tokens after the relink: %q, %v", got, err)
	}
}

func newRefreshApp(t *testing.T, g *gate, refreshes *atomic.Int64) (*App, *config.Bank) {
	t.Helper()
	a := newApp(t, func(*http.Request) (*http.Response, error) {
		n := refreshes.Add(1)
		g.hold()
		return bankReply(http.StatusOK, fmt.Sprintf(`{"access_token":"new-at-%d","refresh_token":"new-rt-%d","expires_in":3600,"refresh_expires_in":7200}`, n, n)), nil
	})
	a.st.Connections["con_1"] = &Connection{ID: "con_1", PartnerID: "p1", Institution: "bank-a", Status: "active"}
	a.seedToken(t, "con_1", "old-at", -time.Hour)
	return a, a.cfg.Banks[0]
}

func TestConcurrentCallersRefreshAnExpiredTokenOnce(t *testing.T) {
	const callers = 8
	var refreshes atomic.Int64
	g := newGate(t)
	a, b := newRefreshApp(t, g, &refreshes)
	var wg sync.WaitGroup
	tokens := make([]string, callers)
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tokens[i], errs[i] = a.accessToken("con_1", b, false)
		}()
	}
	g.waitEntered(t)
	for i := 0; i < 1000; i++ {
		runtime.Gosched()
	}
	g.open()
	wg.Wait()
	if got := refreshes.Load(); got != 1 {
		t.Fatalf("%d refresh calls, want 1", got)
	}
	for i := range callers {
		if errs[i] != nil || tokens[i] != "new-at-1" {
			t.Fatalf("caller %d got %q, %v, want new-at-1", i, tokens[i], errs[i])
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.refreshing) != 0 {
		t.Fatalf("%d refreshes still in flight", len(a.refreshing))
	}
}

func TestTokenIsReusedOrRefreshedByItsExpiry(t *testing.T) {
	for _, c := range []struct {
		name    string
		expires time.Duration
		usable  bool
		due     bool
	}{
		{"expired an hour ago", -time.Hour, false, true},
		{"inside the margin", refreshMargin - 10*time.Second, false, true},
		{"just past the margin", refreshMargin + 10*time.Second, true, true},
		{"inside the look-ahead", refreshAhead - 10*time.Second, true, true},
		{"just past the look-ahead", refreshAhead + 10*time.Second, true, false},
		{"far future", 100 * 365 * 24 * time.Hour, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, use := range []string{"access", "refresh ahead"} {
				var refreshes atomic.Int64
				g := newGate(t)
				g.open()
				a, b := newRefreshApp(t, g, &refreshes)
				a.seedToken(t, "con_1", "old-at", c.expires)
				want, wantTok := int64(0), "old-at"
				if use == "access" {
					if !c.usable {
						want, wantTok = 1, "new-at-1"
					}
					if tok, err := a.accessToken("con_1", b, false); err != nil || tok != wantTok {
						t.Fatalf("access token %q, %v, want %q", tok, err, wantTok)
					}
				} else {
					if c.due {
						want = 1
					}
					a.refreshConnectionToken("con_1")
				}
				if n := refreshes.Load(); n != want {
					t.Fatalf("%s: %d refresh calls, want %d", use, n, want)
				}
			}
		})
	}
}

func TestConcurrentCallersNeverOverwriteNewerTokens(t *testing.T) {
	for _, tc := range []struct {
		name      string
		during    bool
		wantCalls int64
	}{
		{"late caller whose entry was replaced", false, 0},
		{"relinked while the bank answers", true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var refreshes atomic.Int64
			g := newGate(t)
			a, b := newRefreshApp(t, g, &refreshes)
			a.mu.Lock()
			seen := a.st.Vault["con_1"]
			a.mu.Unlock()
			done := make(chan error, 1)
			if tc.during {
				go func() { done <- a.refreshToken("con_1", b, seen) }()
				g.waitEntered(t)
			}
			a.mu.Lock()
			a.seedToken(t, "con_1", "relinked-at", time.Hour)
			a.mu.Unlock()
			g.open()
			if !tc.during {
				done <- a.refreshToken("con_1", b, seen)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("refresh: %v", err)
				}
			case <-time.After(wait):
				t.Fatal("the refresh never finished")
			}
			a.mu.Lock()
			defer a.mu.Unlock()
			got, err := a.openAccessLocked("con_1")
			if refreshes.Load() != tc.wantCalls || err != nil || got != "relinked-at" || len(a.refreshing) != 0 {
				t.Fatalf("%d refresh calls, access token %q, %v, %d in flight, want %d calls and relinked-at", refreshes.Load(), got, err, len(a.refreshing), tc.wantCalls)
			}
		})
	}
}
