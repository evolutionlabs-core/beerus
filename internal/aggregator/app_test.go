package aggregator

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

const (
	testToken = "access-test"
	wait      = 5 * time.Second
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func bankReply(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
}

func newApp(t *testing.T, rt roundTrip, partners ...*config.Partner) *App {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	chk(t, err)
	return &App{
		cfg:    &config.Config{Banks: []*config.Bank{{ID: "bank-a", Port: 7001, APIPort: 7011, HistoryDays: 36500}}, Partners: partners},
		st:     newState(),
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		clock:  clock.New(),
		vault:  &vault{master: bytes.Repeat([]byte{7}, vaultKeyBytes)},
		obKey:  key,
		obHTTP: &http.Client{Transport: rt},
	}
}

func (a *App) seedToken(t *testing.T, id, access string, accessLife time.Duration) {
	t.Helper()
	now := a.clock.Real()
	chk(t, a.vaultPut(id, bankToken{Access: access, Refresh: "rt", AccessExpires: now.Add(accessLife), RefreshExpires: now.Add(2 * time.Hour)}))
}

func (a *App) seedAccount(c *Connection, nuban string) *AccountRec {
	id := AccountID(c.PartnerID, c.Institution, nuban)
	acct := &AccountRec{ID: id, PartnerID: c.PartnerID, Key: acctKey(c.Institution, nuban), ConnectionID: c.ID, NUBAN: nuban, Grants: []string{c.ID}}
	c.AccountIDs = []string{id}
	a.st.Connections[c.ID] = c
	a.st.Accounts[id] = acct
	return acct
}

type gate struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGate(t *testing.T) *gate {
	g := &gate{entered: make(chan struct{}, 16), release: make(chan struct{})}
	t.Cleanup(g.open)
	return g
}

func (g *gate) open() { g.once.Do(func() { close(g.release) }) }

func (g *gate) hold() {
	g.entered <- struct{}{}
	<-g.release
}

func (g *gate) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-g.entered:
	case <-time.After(wait):
		t.Fatal("the bank was never called")
	}
}

type partnerReply struct {
	code   int
	header http.Header
	body   bytes.Buffer
}

func (r *partnerReply) Header() http.Header { return r.header }

func (r *partnerReply) Write(b []byte) (int, error) {
	if r.code == 0 {
		r.code = http.StatusOK
	}
	return r.body.Write(b)
}

func (r *partnerReply) WriteHeader(code int) { r.code = code }

func callPartner(t *testing.T, p *config.Partner, h http.HandlerFunc, method, target, body, token string, path map[string]string) (int, map[string]any) {
	t.Helper()
	r, err := http.NewRequestWithContext(context.WithValue(t.Context(), ctxPartner, p), method, "http://connect.localhost"+target, strings.NewReader(body))
	chk(t, err)
	if token != "" {
		r.Header.Set(config.AccessTokenHeader, token)
	}
	for k, v := range path {
		r.SetPathValue(k, v)
	}
	w := &partnerReply{header: http.Header{}}
	h(w, r)
	out := map[string]any{}
	if w.body.Len() == 0 {
		return w.code, out
	}
	if err := json.Unmarshal(w.body.Bytes(), &out); err != nil {
		t.Fatalf("reply %d is not JSON: %s", w.code, w.body.String())
	}
	return w.code, out
}

func TestPartnerRequestsAreChecked(t *testing.T) {
	const balanceReply = `{"ledger_balance":{"amount_minor":0},"available_balance":{"amount_minor":0}}`
	p1, p2 := &config.Partner{ID: "p1"}, &config.Partner{ID: "p2"}
	a := newApp(t, func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodDelete:
			return bankReply(http.StatusNoContent, ""), nil
		case strings.HasSuffix(r.URL.Path, "/balances"):
			return bankReply(http.StatusOK, balanceReply), nil
		}
		return bankReply(http.StatusNotFound, "{}"), nil
	}, p1, p2)
	from := a.clock.Today().AddDays(-30)
	for id, token := range map[string]string{"con_1": "access-one", "con_2": "access-two"} {
		c := &Connection{ID: id, PartnerID: "p1", Institution: "bank-a", Status: "active", ConsentID: "obc_" + id, AccessHash: web.HashSecret(token)}
		a.seedAccount(c, map[string]string{"con_1": "0123456789", "con_2": "0123456780"}[id]).From = from
		a.seedToken(t, id, "at", time.Hour)
	}
	a.st.Connections["con_3"] = &Connection{ID: "con_3", PartnerID: "p2", Institution: "bank-a", Status: "active", AccessHash: web.HashSecret("access-three")}
	now := a.clock.Real()
	a.st.LinkSessions["ls_expired"] = &LinkSession{ID: "ls_expired", PartnerID: "p1", Status: "authorised", PublicHash: web.HashSecret("public-old"), PublicExpires: now.Add(-time.Minute)}
	a.st.LinkSessions["ls_used"] = &LinkSession{ID: "ls_used", PartnerID: "p1", Status: "exchanged", PublicUsed: true, PublicHash: web.HashSecret("public-used"), PublicExpires: now.Add(time.Minute)}
	a.st.LinkSessions["ls_other"] = &LinkSession{ID: "ls_other", PartnerID: "p2", Status: "authorised", PublicHash: web.HashSecret("public-other"), PublicExpires: now.Add(time.Minute)}

	own := AccountID("p1", "bank-a", "0123456789")
	balance := "/v1/accounts/" + own + "/balance?as_of=" + a.clock.Today().AddDays(-1).String()
	const invalid, notKnown = "This access_token is not valid.", "This public_token is not known."
	rows := []struct {
		name, token, target, body, message string
		handler                            http.HandlerFunc
		method, code                       string
		status                             int
	}{
		{"no token", "", balance, "", "Send the connection's access_token in the Beerus-Access-Token header.", a.getBalance, http.MethodGet, "invalid_request", http.StatusBadRequest},
		{"wrong token", "access-nope", balance, "", invalid, a.getBalance, http.MethodGet, "invalid_access_token", http.StatusUnauthorized},
		{"another partner's token", "access-three", balance, "", invalid, a.getBalance, http.MethodGet, "invalid_access_token", http.StatusUnauthorized},
		{"an account outside the connection", "access-two", balance, "", "No such account.", a.getBalance, http.MethodGet, "not_found", http.StatusNotFound},
		{"a good balance", "access-one", balance, "", "", a.getBalance, http.MethodGet, "", http.StatusOK},
		{"remove", "access-one", "/v1/connections/remove", "", "", a.removeConnection, http.MethodPost, "", http.StatusNoContent},
		{"the token of a removed connection", "access-one", balance, "", invalid, a.getBalance, http.MethodGet, "invalid_access_token", http.StatusUnauthorized},
		{"the other connection", "access-two", "/v1/accounts", "", "", a.listAccounts, http.MethodGet, "", http.StatusOK},
		{"exchange with no token", "", "/v1/public_token/exchange", `{}`, "The body must be a JSON object with public_token.", a.exchange, http.MethodPost, "invalid_request", http.StatusBadRequest},
		{"exchange of an unknown token", "", "/v1/public_token/exchange", `{"public_token":"public-nope"}`, notKnown, a.exchange, http.MethodPost, "invalid_public_token", http.StatusNotFound},
		{"exchange of another partner's token", "", "/v1/public_token/exchange", `{"public_token":"public-other"}`, notKnown, a.exchange, http.MethodPost, "invalid_public_token", http.StatusNotFound},
		{"exchange of an expired token", "", "/v1/public_token/exchange", `{"public_token":"public-old"}`, "This public_token has expired. Start a new link.", a.exchange, http.MethodPost, "invalid_public_token", http.StatusNotFound},
		{"exchange of a used token", "", "/v1/public_token/exchange", `{"public_token":"public-used"}`, "This public_token was already used.", a.exchange, http.MethodPost, "invalid_public_token", http.StatusNotFound},
	}
	for _, c := range rows {
		status, body := callPartner(t, p1, c.handler, c.method, c.target, c.body, c.token, map[string]string{"id": own})
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

func chk(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
