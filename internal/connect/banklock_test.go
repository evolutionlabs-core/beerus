package connect

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"strings"
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
				clock: clock.New(clock.State{Pace: 1}, func() time.Time { return now }),
				vault: &vault{master: bytes.Repeat([]byte{7}, vaultKeyBytes)},
				obHTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
					switch {
					case strings.HasSuffix(r.URL.Path, "/discover"):
						return reply(`{"stable_ids":true}`), nil
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
			acct := &AccountRec{ID: "acc_1", PartnerID: "p1", WorldKey: "w1", ConnectionID: "con_1", NUBAN: "0123456789", Grants: []string{"con_1"}}
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
