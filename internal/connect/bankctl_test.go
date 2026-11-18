package connect

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

func adminAt(t *testing.T, routes map[string]string) *bankAdmin {
	t.Helper()
	c := controlAt(t, routes)
	return &bankAdmin{link: c.link, id: "bank-a", name: "Bank A", base: c.base}
}

func TestBankAdminReadsTheBooksOverTheControlAPI(t *testing.T) {
	entry := `{"id":"ln_1","account":"0000000014","seq":3,"amount_minor":-5000,"balance_after_minor":95000,"posted_at":"2026-11-18T09:30:00+01:00","value_date":"2026-11-18","narration":"NIP transfer","channel":"nip","session_id":"000001260101090000000000000001","reference":"ref-1","counterparty":{"name":"Ada Obi","account_number":"0000000022","bank_code":"000002","bank_name":"Bank B"}}`
	routes := map[string]string{
		"/v1/books/totals":                              `{"customers":2,"accounts":3,"deposits_minor":150000}`,
		"/v1/closure-payouts":                           `{"data":[{"account_number":"0000000014","paid_minor":500},{"account_number":"0000000022","paid_minor":700}]}`,
		"/v1/ledger/check":                              `{"ok":false,"trial_balance_minor":5,"balance_mismatches":["a"],"event_mismatches":[],"unbalanced_entries":[7]}`,
		"/v1/books/accounts/0000000014/entries":         `{"data":[` + entry + `],"next_cursor":"ln_1"}`,
		"/v1/books/accounts/0000000014/entries/ln_1":    entry,
		"/v1/accounts/0000000014":                       `{"nuban":"0000000014","name":"Ada Obi","customer":"cus_1","type":"current","status":"frozen","currency":"NGN","balance_minor":95000}`,
		"/v1/logins":                                    `{"customer_id":"cus_1"}`,
		"/v1/books/sessions/000001260101090000000000000001": `{"data":[` + entry + `]}`,
	}
	c := adminAt(t, routes)
	ctx := t.Context()
	posted := time.Date(2026, 11, 18, 9, 30, 0, 0, clock.Lagos)
	line := bank.FeedLine{Key: "ln_1", NUBAN: "0000000014", Amount: -5000, BookedAt: posted, ValueDate: clock.Date{Y: 2026, M: time.November, D: 18}, Narration: "NIP transfer", Counterparty: &bank.Counterparty{Name: "Ada Obi", AccountNumber: "0000000022", BankCode: "000002", BankName: "Bank B"}, SessionID: "000001260101090000000000000001", BankRef: "ref-1", Channel: "nip", BalanceAfter: 95000, Seq: 3}
	rows := []struct {
		name string
		got  func() (string, error)
		want string
	}{
		{"totals", func() (string, error) { v, err := c.totals(ctx); return fmt.Sprint(v), err }, "{2 3 150000}"},
		{"payouts", func() (string, error) { v, err := c.payouts(ctx); return fmt.Sprint(v), err }, "map[0000000014:500 0000000022:700]"},
		{"check", func() (string, error) { v, err := c.check(ctx); return v.String(), err }, "trial balance 5, balance mismatches [a], event mismatches [], unbalanced entries [7]"},
		{"account", func() (string, error) { v, err := c.account(ctx, "0000000014"); return fmt.Sprint(v), err }, "{0000000014 Ada Obi cus_1 current frozen NGN 95000}"},
		{"login", func() (string, error) { return c.login(ctx, "ada", "secret") }, "cus_1"},
		{"entries", func() (string, error) {
			v, next, err := c.recent(ctx, "0000000014", 50, "")
			return fmt.Sprint(v[0].Equal(line), next), err
		}, "true ln_1"},
		{"entry", func() (string, error) { v, err := c.line(ctx, "0000000014", "ln_1"); return fmt.Sprint(v.Equal(line)), err }, "true"},
		{"session", func() (string, error) {
			v, err := c.sessionLines(ctx, "000001260101090000000000000001")
			return fmt.Sprint(len(v), v[0].Equal(line)), err
		}, "1 true"},
	}
	for _, r := range rows {
		got, err := r.got()
		if err != nil || got != r.want {
			t.Errorf("%s: got %q, %v; want %q", r.name, got, err, r.want)
		}
	}
}

func TestBankAdminWhenTheBankIsAway(t *testing.T) {
	a := &App{cfg: &config.Config{Banks: []*config.Bank{{ID: "bank-a", Name: "Bank A", Port: 1}}}, link: server.NewLink("", "")}
	c, err := a.adminOf("bank-a")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.totals(t.Context())
	var down *bankDownError
	if !errors.As(err, &down) || down.Name != "Bank A" {
		t.Fatalf("got %v, want a down error for Bank A", err)
	}
	rec := httptest.NewRecorder()
	a.log = nopLogger()
	a.bankError(rec, httptest.NewRequest(http.MethodGet, "/banks/bank-a/close-impact", nil), err)
	want := `{"error":{"code":"unavailable","message":"Bank A is not running or cannot be reached. Run beerus get banks to see its state, then try again.","request_id":""}}`
	if rec.Code != http.StatusServiceUnavailable || rec.Body.String() != want+"\n" {
		t.Fatalf("got %d %s, want 503 %s", rec.Code, rec.Body.String(), want)
	}
}
