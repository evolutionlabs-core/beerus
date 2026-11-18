package connect

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

type bankPosting struct {
	date   string
	amount int64
}

func TestReconnectWithLaterStartKeepsTheEarlierFromAndTheBalanceInvariant(t *testing.T) {
	const opening = int64(10_000)
	postings := []bankPosting{{"2026-10-20", 5000}, {"2026-10-27", -2000}, {"2026-11-03", 7000}, {"2026-11-10", -1500}}
	balanceOn := func(date string) int64 {
		sum := opening
		for _, p := range postings {
			if p.date <= date {
				sum += p.amount
			}
		}
		return sum
	}
	const last = "9999-12-31"
	bank := roundTrip(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/transactions"):
			from := r.URL.Query().Get("from")
			var rows []string
			for i, p := range postings {
				if p.date < from {
					continue
				}
				rows = append(rows, fmt.Sprintf(`{"id":"t%d","sequence":%d,"status":"posted","amount_minor":%d,"booked_at":"%sT10:00:00Z","value_date":"%s","description":"posting %d","channel":"nip","balance_after_minor":%d}`, i+1, i+1, p.amount, p.date, p.date, i+1, balanceOn(p.date)))
			}
			return bankReply(http.StatusOK, `{"data":[`+strings.Join(rows, ",")+`],"next_cursor":null}`), nil
		case strings.HasSuffix(r.URL.Path, "/balances"):
			date := r.URL.Query().Get("as_of")
			if date == "" {
				date = last
			}
			amount := balanceOn(date)
			return bankReply(http.StatusOK, fmt.Sprintf(`{"ledger_balance":{"amount_minor":%d},"available_balance":{"amount_minor":%d}}`, amount, amount)), nil
		}
		return bankReply(http.StatusNotFound, "{}"), nil
	})
	partner := &config.Partner{ID: "p1", RedirectURIs: []string{"https://app.example.localhost/cb"}, WebhookURL: "https://hooks.example.localhost/wh"}
	a := &App{
		cfg:    &config.Config{Banks: []*config.Bank{{ID: "bank-a", Port: 7001, APIPort: 7011, HistoryDays: 36500}}, Partners: []*config.Partner{partner}},
		st:     newState(),
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		clock:  clock.New(),
		vault:  &vault{master: bytes.Repeat([]byte{7}, vaultKeyBytes)},
		obHTTP: &http.Client{Transport: bank},
	}
	now := a.clock.Real()
	key := acctKey("bank-a", "0123456789")
	accID := AccountID("p1", "bank-a", "0123456789")
	link := func(id string, start clock.Date) *LinkSession {
		return &LinkSession{ID: id, PartnerID: "p1", WorkspaceID: "ws_1", Institution: "bank-a", StartDate: start, Accounts: []string{key}}
	}
	connect := func(id, consent string, ls *LinkSession) *Connection {
		t.Helper()
		c := &Connection{ID: id, PartnerID: "p1", WorkspaceID: "ws_1", Institution: "bank-a", StartDate: ls.StartDate, ConsentID: consent}
		a.mu.Lock()
		a.st.Connections[id] = c
		auth := a.authoriseLocked(c, ls)
		a.finishAuthoriseLocked(c, auth)
		err := a.vaultPut(id, bankToken{Access: "at-" + id, Refresh: "rt", AccessExpires: now.Add(time.Hour), RefreshExpires: now.Add(2 * time.Hour)})
		a.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		if !a.syncConnection(c, a.clock.Now()) {
			t.Fatalf("the sync of %s did not finish", id)
		}
		return c
	}

	first := connect("con_1", "obc_1", link("lks_1", clock.Date{Y: 2026, M: time.October, D: 18}))
	rec := a.st.Accounts[accID]
	if rec.From.String() != "2026-10-18" || len(a.st.Feeds[accID].Lines) != 4 {
		t.Fatalf("first connection: from %s with %d lines, want 2026-10-18 with 4", rec.From, len(a.st.Feeds[accID].Lines))
	}
	a.mu.Lock()
	a.setStatus(first, "revoked", "connection_revoked")
	a.mu.Unlock()

	later := link("lks_2", clock.Date{Y: 2026, M: time.November, D: 5})
	later.BankFrom = later.StartDate
	second := connect("con_2", "obc_2", later)
	if rec.From.String() != "2026-10-18" || !rec.Cut || second.EffectiveStart == nil || second.EffectiveStart.String() != "2026-10-18" {
		t.Fatalf("after reconnecting from 2026-11-05: account from %s, cut %v, connection effective start %v, want 2026-10-18 for all", rec.From, rec.Cut, second.EffectiveStart)
	}

	var sum int64
	posted := 0
	for _, line := range a.st.Feeds[accID].Lines {
		if line.Status == "posted" {
			sum += line.AmountMinor
			posted++
		}
	}
	status, body := callPartner(t, partner, a.getBalance, http.MethodGet, "/v1/accounts/"+accID+"/balance?as_of="+rec.From.AddDays(-1).String(), "", map[string]string{"id": accID})
	ledger, _ := body["ledger_balance"].(map[string]any)
	if status != http.StatusOK || body["effective_start_date"] != "2026-10-18" || ledger["amount_minor"] != float64(opening) {
		t.Fatalf("balance before the first date: %d %v, want 200 with %d and effective_start_date 2026-10-18", status, body, opening)
	}
	if posted != 4 || sum != 8500 || opening+sum != rec.Ledger || rec.Ledger != 18_500 {
		t.Fatalf("opening %d + %d posted lines worth %d != ledger %d, want 4 lines worth 8500 and ledger 18500", opening, posted, sum, rec.Ledger)
	}
}
