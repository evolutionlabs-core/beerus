package aggregator

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
	"github.com/evolutionlabs-core/beerus/internal/web"
)

func TestAccessTokenScopesEveryConnectionCall(t *testing.T) {
	const balanceReply = `{"ledger_balance":{"amount_minor":0},"available_balance":{"amount_minor":0}}`
	p1, p2 := &config.Partner{ID: "p1"}, &config.Partner{ID: "p2"}
	a := &App{
		cfg:   &config.Config{Banks: []*config.Bank{{ID: "bank-a", Port: 7001, APIPort: 7011, HistoryDays: 36500}}, Partners: []*config.Partner{p1, p2}},
		st:    newState(),
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		clock: clock.New(),
		vault: &vault{master: bytes.Repeat([]byte{7}, vaultKeyBytes)},
		obHTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodDelete {
				return bankReply(http.StatusNoContent, ""), nil
			}
			if strings.HasSuffix(r.URL.Path, "/balances") {
				return bankReply(http.StatusOK, balanceReply), nil
			}
			return bankReply(http.StatusNotFound, "{}"), nil
		})},
	}
	now := a.clock.Real()
	from := a.clock.Today().AddDays(-30)
	own := AccountID("p1", "bank-a", "0123456789")
	sibling := AccountID("p1", "bank-a", "0123456780")
	for id, token := range map[string]string{"con_1": "access-one", "con_2": "access-two"} {
		a.st.Connections[id] = &Connection{ID: id, PartnerID: "p1", Institution: "bank-a", Status: "active", ConsentID: "obc_" + id, AccessHash: web.HashSecret(token)}
		if err := a.vaultPut(id, bankToken{Access: "at", Refresh: "rt", AccessExpires: now.Add(time.Hour), RefreshExpires: now.Add(2 * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	a.st.Connections["con_3"] = &Connection{ID: "con_3", PartnerID: "p2", Institution: "bank-a", Status: "active", AccessHash: web.HashSecret("access-three")}
	a.st.Connections["con_1"].AccountIDs = []string{own}
	a.st.Connections["con_2"].AccountIDs = []string{sibling}
	a.st.Accounts[own] = &AccountRec{ID: own, PartnerID: "p1", Key: acctKey("bank-a", "0123456789"), ConnectionID: "con_1", NUBAN: "0123456789", Grants: []string{"con_1"}, From: from}
	a.st.Accounts[sibling] = &AccountRec{ID: sibling, PartnerID: "p1", Key: acctKey("bank-a", "0123456780"), ConnectionID: "con_2", NUBAN: "0123456780", Grants: []string{"con_2"}, From: from}

	balance := "/v1/accounts/" + own + "/balance?as_of=" + a.clock.Today().AddDays(-1).String()
	rows := []struct {
		name, token, target, message string
		handler                      http.HandlerFunc
		method, code                 string
		status                       int
	}{
		{"no token", "", balance, "Send the connection's access_token in the Beerus-Access-Token header.", a.getBalance, http.MethodGet, "invalid_request", http.StatusBadRequest},
		{"wrong token", "access-nope", balance, "This access_token is not valid.", a.getBalance, http.MethodGet, "invalid_access_token", http.StatusUnauthorized},
		{"another partner's token", "access-three", balance, "This access_token is not valid.", a.getBalance, http.MethodGet, "invalid_access_token", http.StatusUnauthorized},
		{"an account outside the connection", "access-two", balance, "No such account.", a.getBalance, http.MethodGet, "not_found", http.StatusNotFound},
		{"a good balance", "access-one", balance, "", a.getBalance, http.MethodGet, "", http.StatusOK},
		{"remove", "access-one", "/v1/connections/remove", "", a.removeConnection, http.MethodPost, "", http.StatusNoContent},
		{"the token of a removed connection", "access-one", balance, "This access_token is not valid.", a.getBalance, http.MethodGet, "invalid_access_token", http.StatusUnauthorized},
		{"the other connection", "access-two", "/v1/accounts", "", a.listAccounts, http.MethodGet, "", http.StatusOK},
	}
	for _, c := range rows {
		status, body := callPartner(t, p1, c.handler, c.method, c.target, "", c.token, map[string]string{"id": own})
		apiErr, _ := body["error"].(map[string]any)
		code, message := "", ""
		if apiErr != nil {
			code, message = apiErr["code"].(string), apiErr["message"].(string)
		}
		if status != c.status || code != c.code || message != c.message {
			t.Errorf("%s: %d %v, want %d %s %q", c.name, status, body, c.status, c.code, c.message)
		}
	}
}
