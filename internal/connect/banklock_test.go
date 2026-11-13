package connect

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

func TestBankCallsDoNotHoldTheLockAndLateResultsAreDropped(t *testing.T) {
	const wait = 5 * time.Second
	for _, tc := range []struct {
		name   string
		revoke bool
		synced bool
		lines  int
		ledger int64
	}{
		{name: "untouched while the bank answers", synced: true, lines: 1, ledger: 5000},
		{name: "revoked while the bank answers", revoke: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
			entered := make(chan struct{})
			release := make(chan struct{})
			reply := func(body string) *http.Response {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
			}
			a := &App{
				cfg:   &config.Config{Banks: []*config.Bank{{ID: "bank-a", Port: 7001, APIPort: 7011}}},
				st:    newState(),
				log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
				clock: clock.New(func() time.Time { return now }),
				vault: &vault{master: bytes.Repeat([]byte{7}, vaultKeyBytes)},
				obHTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
					switch {
					case strings.HasSuffix(r.URL.Path, "/transactions"):
						entered <- struct{}{}
						<-release
						return reply(`{"data":[{"id":"t1","sequence":1,"status":"posted","amount_minor":5000,"booked_at":"2026-01-02T10:00:00Z","value_date":"2026-01-02","description":"salary","channel":"nip","balance_after_minor":5000}],"next_cursor":null}`), nil
					case strings.HasSuffix(r.URL.Path, "/balances"):
						return reply(`{"ledger_balance":{"amount_minor":5000},"available_balance":{"amount_minor":5000}}`), nil
					}
					return &http.Response{StatusCode: http.StatusNotFound, Body: http.NoBody, Header: http.Header{}}, nil
				})},
			}
			c := &Connection{ID: "con_1", PartnerID: "p1", Institution: "bank-a", Status: "active", ConsentID: "obc_1", AccountIDs: []string{"acc_1"}}
			acct := &AccountRec{ID: "acc_1", PartnerID: "p1", Key: acctKey("bank-a", "0123456789"), ConnectionID: "con_1", NUBAN: "0123456789", Grants: []string{"con_1"}}
			a.st.Connections[c.ID] = c
			a.st.Accounts[acct.ID] = acct
			if err := a.vaultPut(c.ID, bankToken{Access: "at", Refresh: "rt", AccessExpires: now.Add(time.Hour), RefreshExpires: now.Add(2 * time.Hour)}); err != nil {
				t.Fatalf("store token: %v", err)
			}
			done := make(chan bool)
			go func() { done <- a.syncConnection(c, now) }()
			select {
			case <-entered:
			case <-time.After(wait):
				t.Fatal("the bank was never called")
			}
			locked := make(chan struct{})
			go func() {
				a.mu.Lock()
				if tc.revoke {
					a.setStatus(c, "revoked", "connection_revoked")
				}
				close(locked)
				a.mu.Unlock()
			}()
			select {
			case <-locked:
			case <-time.After(wait):
				t.Fatal("the lock was held across the bank call")
			}
			close(release)
			var synced bool
			select {
			case synced = <-done:
			case <-time.After(wait):
				t.Fatal("the sync never finished")
			}
			a.mu.Lock()
			defer a.mu.Unlock()
			if synced != tc.synced {
				t.Fatalf("sync result %v, want %v", synced, tc.synced)
			}
			lines := 0
			if f := a.st.Feeds[acct.ID]; f != nil {
				lines = len(f.Lines)
			}
			if lines != tc.lines || acct.Ledger != tc.ledger {
				t.Fatalf("applied %d lines and ledger %d, want %d and %d", lines, acct.Ledger, tc.lines, tc.ledger)
			}
			types := map[string]int{}
			for _, d := range a.st.Deliveries {
				types[d.Type]++
			}
			wantTypes := map[string]int{}
			if tc.synced {
				wantTypes["historical_sync.completed"], wantTypes["sync.available"] = 1, 1
			} else {
				wantTypes["connection.status_changed"] = 1
			}
			if len(types) != len(wantTypes) {
				t.Fatalf("events %v, want %v", types, wantTypes)
			}
			for k, v := range wantTypes {
				if types[k] != v {
					t.Fatalf("events %v, want %v", types, wantTypes)
				}
			}
		})
	}
}

func TestRevokeKeepsTokensOfAConnectionRelinkedMeanwhile(t *testing.T) {
	const wait = 5 * time.Second
	now := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
	entered := make(chan struct{})
	release := make(chan struct{})
	a := &App{
		cfg:   &config.Config{Banks: []*config.Bank{{ID: "bank-a", Port: 7001, APIPort: 7011}}},
		st:    newState(),
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		clock: clock.New(func() time.Time { return now }),
		vault: &vault{master: bytes.Repeat([]byte{7}, vaultKeyBytes)},
		obHTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			entered <- struct{}{}
			<-release
			return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody, Header: http.Header{}}, nil
		})},
	}
	c := &Connection{ID: "con_1", PartnerID: "p1", Institution: "bank-a", Status: "active", ConsentID: "obc_1"}
	a.st.Connections[c.ID] = c
	token := func(access string) bankToken {
		return bankToken{Access: access, Refresh: "rt", AccessExpires: now.Add(time.Hour), RefreshExpires: now.Add(2 * time.Hour)}
	}
	if err := a.vaultPut(c.ID, token("old-at")); err != nil {
		t.Fatalf("store token: %v", err)
	}
	done := make(chan error)
	go func() { done <- a.revokeConnectionAtBank(c.ID) }()
	select {
	case <-entered:
	case <-time.After(wait):
		t.Fatal("the bank was never called")
	}
	a.mu.Lock()
	c.ConsentID = "obc_2"
	err := a.vaultPut(c.ID, token("new-at"))
	a.mu.Unlock()
	if err != nil {
		t.Fatalf("store new token: %v", err)
	}
	close(release)
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
	got, err := a.openAccessLocked(c.ID)
	if err != nil || got != "new-at" {
		t.Fatalf("tokens after the relink: %q, %v", got, err)
	}
}

func newRefreshApp(t *testing.T, now time.Time, refreshes *atomic.Int64, entered chan struct{}, release chan struct{}) (*App, *config.Bank) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("signing key: %v", err)
	}
	b := &config.Bank{ID: "bank-a", Port: 7001, APIPort: 7011}
	a := &App{
		cfg:   &config.Config{Banks: []*config.Bank{b}},
		st:    newState(),
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		clock: clock.New(func() time.Time { return now }),
		vault: &vault{master: bytes.Repeat([]byte{7}, vaultKeyBytes)},
		obKey: key,
		obHTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			n := refreshes.Add(1)
			entered <- struct{}{}
			<-release
			body := fmt.Sprintf(`{"access_token":"new-at-%d","refresh_token":"new-rt-%d","expires_in":3600,"refresh_expires_in":7200}`, n, n)
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
		})},
	}
	a.st.Connections["con_1"] = &Connection{ID: "con_1", PartnerID: "p1", Institution: "bank-a", Status: "active"}
	if err := a.vaultPut("con_1", bankToken{Access: "old-at", Refresh: "old-rt", AccessExpires: now.Add(-time.Hour), RefreshExpires: now.Add(time.Hour)}); err != nil {
		t.Fatalf("store token: %v", err)
	}
	return a, b
}

func TestConcurrentCallersRefreshAnExpiredTokenOnce(t *testing.T) {
	const (
		wait    = 5 * time.Second
		callers = 8
	)
	now := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
	var refreshes atomic.Int64
	entered := make(chan struct{}, callers)
	release := make(chan struct{})
	a, b := newRefreshApp(t, now, &refreshes, entered, release)
	start := make(chan struct{})
	var ready sync.WaitGroup
	var wg sync.WaitGroup
	tokens := make([]string, callers)
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		ready.Add(1)
		go func() {
			defer wg.Done()
			ready.Done()
			<-start
			tokens[i], errs[i] = a.accessToken("con_1", b, false)
		}()
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		wg.Wait()
	})
	ready.Wait()
	close(start)
	select {
	case <-entered:
	case <-time.After(wait):
		t.Fatal("the bank was never called")
	}
	for i := 0; i < 1000; i++ {
		runtime.Gosched()
	}
	close(release)
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

func TestConcurrentCallersNeverOverwriteNewerTokens(t *testing.T) {
	const wait = 5 * time.Second
	now := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name             string
		relinkDuringCall bool
		wantCalls        int64
		wantAccess       string
	}{
		{name: "late caller whose entry was replaced", wantCalls: 0, wantAccess: "relinked-at"},
		{name: "relinked while the bank answers", relinkDuringCall: true, wantCalls: 1, wantAccess: "relinked-at"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var refreshes atomic.Int64
			entered := make(chan struct{}, 2)
			release := make(chan struct{})
			a, b := newRefreshApp(t, now, &refreshes, entered, release)
			relinked := bankToken{Access: "relinked-at", Refresh: "relinked-rt", AccessExpires: now.Add(time.Hour), RefreshExpires: now.Add(2 * time.Hour)}
			a.mu.Lock()
			seen := a.st.Vault["con_1"]
			a.mu.Unlock()
			done := make(chan error, 1)
			if tc.relinkDuringCall {
				go func() { done <- a.refreshToken("con_1", b, seen) }()
				select {
				case <-entered:
				case <-time.After(wait):
					t.Fatal("the bank was never called")
				}
			}
			a.mu.Lock()
			err := a.vaultPut("con_1", relinked)
			a.mu.Unlock()
			if err != nil {
				t.Fatalf("store relinked token: %v", err)
			}
			close(release)
			if !tc.relinkDuringCall {
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
			if got := refreshes.Load(); got != tc.wantCalls {
				t.Fatalf("%d refresh calls, want %d", got, tc.wantCalls)
			}
			a.mu.Lock()
			defer a.mu.Unlock()
			got, err := a.openAccessLocked("con_1")
			if err != nil || got != tc.wantAccess {
				t.Fatalf("access token %q, %v, want %q", got, err, tc.wantAccess)
			}
			if len(a.refreshing) != 0 {
				t.Fatalf("%d refreshes still in flight", len(a.refreshing))
			}
		})
	}
}
