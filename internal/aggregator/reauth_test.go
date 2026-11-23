package aggregator

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

func TestConsentExpiryAtTheBankExpiresTheConnectionUntilReauth(t *testing.T) {
	const (
		expiredReply = `{"error":"access_denied","error_description":"The consent must be renewed.","consent_error":"consent_reconsent_required"}`
		okReply      = `{"data":[],"next_cursor":null}`
		balanceReply = `{"ledger_balance":{"amount_minor":0},"available_balance":{"amount_minor":0}}`
	)
	renewed := false
	partner := &config.Partner{ID: "p1", RedirectURIs: []string{"https://app.example.localhost/cb"}, WebhookURL: "https://hooks.example.localhost/wh"}
	a := &App{
		cfg:   &config.Config{Banks: []*config.Bank{{ID: "bank-a", Port: 7001, APIPort: 7011, HistoryDays: 36500}}, Partners: []*config.Partner{partner}},
		st:    newState(),
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		clock: clock.New(),
		vault: &vault{master: bytes.Repeat([]byte{7}, vaultKeyBytes)},
		obHTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			switch {
			case r.Method == http.MethodDelete:
				return bankReply(http.StatusNoContent, ""), nil
			case !renewed:
				return bankReply(http.StatusForbidden, expiredReply), nil
			case strings.HasSuffix(r.URL.Path, "/transactions"):
				return bankReply(http.StatusOK, okReply), nil
			case strings.HasSuffix(r.URL.Path, "/balances"):
				return bankReply(http.StatusOK, balanceReply), nil
			}
			return bankReply(http.StatusNotFound, "{}"), nil
		})},
	}
	now := a.clock.Real()
	account := acctKey("bank-a", "0123456789")
	accID := AccountID("p1", "bank-a", "0123456789")
	c := &Connection{ID: "con_1", PartnerID: "p1", Institution: "bank-a", Status: "active", ConsentID: "obc_1", AccountIDs: []string{accID}, AccessHash: web.HashSecret(testToken)}
	acct := &AccountRec{ID: accID, PartnerID: "p1", Key: account, ConnectionID: "con_1", NUBAN: "0123456789", Grants: []string{"con_1"}, HistoryDone: true}
	a.st.Connections[c.ID] = c
	a.st.Accounts[acct.ID] = acct
	if err := a.vaultPut(c.ID, bankToken{Access: "at", Refresh: "rt", AccessExpires: now.Add(time.Hour), RefreshExpires: now.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}

	if a.syncConnection(c, a.clock.Now()) {
		t.Fatal("a sync that the bank refused with consent_reconsent_required finished")
	}
	if c.Status != "expired" {
		t.Fatalf("connection status %q, want expired", c.Status)
	}
	events := func() []map[string]any {
		var out []map[string]any
		for _, d := range a.st.Deliveries {
			if d.Type != "connection.status_changed" {
				continue
			}
			var e struct {
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal(d.Body, &e); err != nil {
				t.Fatal(err)
			}
			if d.PartnerID != "p1" || d.Status != "pending" {
				t.Fatalf("delivery for %s with status %s, want a pending one for p1", d.PartnerID, d.Status)
			}
			out = append(out, e.Data)
		}
		return out
	}
	want := []map[string]any{{"connection_id": "con_1", "previous_status": "active", "status": "expired", "error_code": "reauth_required"}}
	if got := events(); !reflect.DeepEqual(got, want) {
		t.Fatalf("webhooks %v, want %v", got, want)
	}

	status, body := callPartner(t, partner, a.syncTransactions, http.MethodGet, "/v1/accounts/"+accID+"/transactions/sync", "", testToken, map[string]string{"id": accID})
	apiErr, _ := body["error"].(map[string]any)
	if status != http.StatusConflict || apiErr["code"] != "reauth_required" || apiErr["message"] != "The customer must consent again." {
		t.Fatalf("a sync on the expired connection: %d %v, want 409 reauth_required %q", status, body, "The customer must consent again.")
	}

	status, body = callPartner(t, partner, a.reauth, http.MethodPost, "/v1/connection/reauth", "", testToken, nil)
	lsID, _ := body["id"].(string)
	ls := a.st.LinkSessions[lsID]
	if status != http.StatusCreated || ls == nil || body["connection_id"] != "con_1" || body["status"] != "created" || ls.ConnectionID != "con_1" || ls.RedirectURI != "https://app.example.localhost/cb" {
		t.Fatalf("reauth: %d %v", status, body)
	}

	renewed = true
	ls.Status, ls.PublicHash, ls.PublicExpires = "authorised", web.HashSecret("public-1"), now.Add(time.Minute)
	ls.Accounts, ls.BankConsentID, ls.ConsentEnds = []string{account}, "obc_2", now.Add(24*time.Hour)
	if err := a.vaultPut(linkVaultPrefix+ls.ID, bankToken{Access: "at2", Refresh: "rt2", AccessExpires: now.Add(time.Hour), RefreshExpires: now.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	status, body = callPartner(t, partner, a.exchange, http.MethodPost, "/v1/public_token/exchange", `{"public_token":"public-1"}`, "", nil)
	if _, issued := body["access_token"]; status != http.StatusOK || body["connection_id"] != "con_1" || issued {
		t.Fatalf("exchange after reauth: %d %v, want 200 with connection con_1 and no new access token", status, body)
	}
	if !a.syncConnection(c, a.clock.Now()) || c.Status != "active" || c.ConsentID != "obc_2" {
		t.Fatalf("after the reauth and a sync: status %q consent %q, want active and obc_2", c.Status, c.ConsentID)
	}
	want = append(want,
		map[string]any{"connection_id": "con_1", "previous_status": "expired", "status": "syncing", "error_code": nil},
		map[string]any{"connection_id": "con_1", "previous_status": "syncing", "status": "active", "error_code": nil},
	)
	if got := events(); !reflect.DeepEqual(got, want) {
		t.Fatalf("webhooks %v, want %v", got, want)
	}
	if status, _ := callPartner(t, partner, a.syncTransactions, http.MethodGet, "/v1/accounts/"+accID+"/transactions/sync", "", testToken, map[string]string{"id": accID}); status != http.StatusOK {
		t.Fatalf("a sync on the renewed connection: %d, want 200", status)
	}
}
