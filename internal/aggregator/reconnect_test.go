package aggregator

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

	for _, c := range []struct {
		id    string
		start clock.Date
		from  string
	}{
		{"con_3", clock.Date{Y: 2026, M: time.October, D: 10}, "2026-10-10"},
		{"con_4", clock.Date{Y: 2026, M: time.October, D: 14}, "2026-10-10"},
		{"con_5", clock.Date{Y: 2026, M: time.October, D: 5}, "2026-10-05"},
	} {
		c3 := connect(c.id, "obc_"+c.id, link("lks_"+c.id, c.start))
		a.mu.Lock()
		a.setStatus(c3, "revoked", "connection_revoked")
		a.mu.Unlock()
		if rec.From.String() != c.from {
			t.Fatalf("after reconnecting from %s: account from %s, want %s", c.start, rec.From, c.from)
		}
	}

	today := a.clock.Today()
	for _, c := range []struct {
		name string
		from clock.Date
		asOf clock.Date
		want int
	}{
		{"the day before the first date", clock.Date{Y: 2026, M: time.March, D: 1}, clock.Date{Y: 2026, M: time.February, D: 28}, http.StatusOK},
		{"two days before the first date", clock.Date{Y: 2026, M: time.March, D: 1}, clock.Date{Y: 2026, M: time.February, D: 27}, http.StatusUnprocessableEntity},
		{"the day before a leap day first date", clock.Date{Y: 2024, M: time.March, D: 1}, clock.Date{Y: 2024, M: time.February, D: 29}, http.StatusOK},
		{"before a leap day first date", clock.Date{Y: 2024, M: time.March, D: 1}, clock.Date{Y: 2024, M: time.February, D: 28}, http.StatusUnprocessableEntity},
		{"the day before a year start first date", clock.Date{Y: 2026, M: time.January, D: 1}, clock.Date{Y: 2025, M: time.December, D: 31}, http.StatusOK},
		{"before a year start first date", clock.Date{Y: 2026, M: time.January, D: 1}, clock.Date{Y: 2025, M: time.December, D: 30}, http.StatusUnprocessableEntity},
		{"yesterday", clock.Date{Y: 2026, M: time.March, D: 1}, today.AddDays(-1), http.StatusOK},
		{"today", clock.Date{Y: 2026, M: time.March, D: 1}, today, http.StatusUnprocessableEntity},
		{"tomorrow", clock.Date{Y: 2026, M: time.March, D: 1}, today.AddDays(1), http.StatusUnprocessableEntity},
		{"a first date in the far future", clock.Date{Y: 2126, M: time.March, D: 1}, clock.Date{Y: 2126, M: time.February, D: 28}, http.StatusUnprocessableEntity},
	} {
		a.mu.Lock()
		rec.From = c.from
		a.mu.Unlock()
		status, body := callPartner(t, partner, a.getBalance, http.MethodGet, "/v1/accounts/"+accID+"/balance?as_of="+c.asOf.String(), "", map[string]string{"id": accID})
		if status != c.want {
			t.Errorf("%s: as_of %s with first date %s gave %d %v, want %d", c.name, c.asOf, c.from, status, body, c.want)
		}
	}
}
