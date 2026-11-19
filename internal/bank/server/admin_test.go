package server

import (
	"context"
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"log/slog"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/banktest"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

const (
	adminToken    = "control-token"
	adminPassword = "correct horse battery"
	adminBVN      = "22912345678"
	otherBVN      = "22987654321"
)

var (
	adminNow     = clock.New().Now()
	requestIDRef = regexp.MustCompile(`"request_id":"req_[a-z0-9]+"`)
	payoutAtRef  = regexp.MustCompile(`("replayed":(?:true|false),)"posted_at":"[^"]*"`)
)

type adminBank struct {
	t     *testing.T
	logs  *strings.Builder
	books *bank.Bank
	srv   *Server
	ada   string
	adaID string
	bola  string
	bolID string
}

type adminReply struct {
	status   int
	replayed bool
	body     string
}

func newAdminBank(t *testing.T) *adminBank {
	t.Helper()
	ctx := context.Background()
	books := bank.New(ledger.New(banktest.Schema(t), nil), bank.Profile{ID: "bank-a", Name: "Bank A", NIPCode: "000014", NUBANCode: "014"}, banktest.Fees(t))
	if err := books.Setup(ctx); err != nil {
		t.Fatal(err)
	}
	if err := books.Genesis(ctx, 1_000_000_000_000, 200_000_000_000, adminNow); err != nil {
		t.Fatal(err)
	}
	hash, err := bank.HashPassword(adminPassword)
	if err != nil {
		t.Fatal(err)
	}
	a := &adminBank{t: t, books: books, logs: &strings.Builder{}}
	ada, err := books.CreateCustomer(ctx, "cus-ada", "ADA OBI", "+2348030000001", clock.Date{Y: 1990, M: 1, D: 1}, adminBVN, "ada", hash, adminNow)
	if err != nil {
		t.Fatal(err)
	}
	bola, err := books.CreateCustomer(ctx, "cus-bola", "BOLA ADE", "+2348030000002", clock.Date{Y: 1991, M: 2, D: 2}, otherBVN, "", "", adminNow)
	if err != nil {
		t.Fatal(err)
	}
	a.adaID, a.bolID = ada.ID, bola.ID
	a.ada = a.open(ada.ID, "acc-ada", 500_000)
	a.bola = a.open(bola.ID, "acc-bola", 100_000)
	a.restart(config.PhaseActive)
	return a
}

func (a *adminBank) open(customer, key string, deposit int64) string {
	a.t.Helper()
	ctx := context.Background()
	acc, err := a.books.OpenAccount(ctx, key, customer, bank.ProductCurrent, adminNow)
	if err != nil {
		a.t.Fatal(err)
	}
	if _, err := a.books.CashDeposit(ctx, "dep-"+key, acc.NUBAN, deposit, adminNow); err != nil {
		a.t.Fatal(err)
	}
	return acc.NUBAN
}

func (a *adminBank) restart(phase string) {
	a.t.Helper()
	a.srv = New(Config{
		Bank:  config.Bank{ID: "bank-a", Name: "Bank A", NIPCode: "000014", NUBANCode: "014"},
		Books: a.books,
		Fees:  banktest.Fees(a.t),
		Link:  control.Link{Token: adminToken},
		Log:   slog.New(slog.NewTextHandler(a.logs, nil)),
		Clock: clock.New(),
		Phase: phase,
	})
}

func (a *adminBank) phase(phase string) {
	a.srv.lifeMu.Lock()
	defer a.srv.lifeMu.Unlock()
	a.srv.phase = phase
}

func (a *adminBank) call(method, path, body, key string) adminReply {
	a.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:1"
	r.Header.Set("Authorization", "Bearer "+adminToken)
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	a.srv.API().ServeHTTP(w, r)
	reply := requestIDRef.ReplaceAllString(strings.TrimSpace(w.Body.String()), `"request_id":"req_ID"`)
	return adminReply{w.Code, w.Header().Get("Idempotent-Replayed") == "true", payoutAtRef.ReplaceAllString(reply, `$1"posted_at":"AT"`)}
}

func apiError(code, message string) string {
	return fmt.Sprintf(`{"error":{"code":%q,"message":%q,"request_id":"req_ID"}}`, code, message)
}

func account(nuban, name, customer, status string, balance int64) string {
	return fmt.Sprintf(`{"nuban":%q,"name":%q,"customer":%q,"type":"current","status":%q,"currency":"NGN","balance_minor":%d}`, nuban, name, customer, status, balance)
}

func TestClosureRoutes(t *testing.T) {
	a := newAdminBank(t)
	payout := func(nuban string, amount int64) string {
		return fmt.Sprintf(`{"account_number":%q,"amount_minor":%d}`, nuban, amount)
	}
	result := func(nuban string, amount, balance int64) string {
		return fmt.Sprintf(`{"account_number":%q,"amount_minor":%d,"balance_minor":%d,"replayed":%t,"posted_at":"AT"}`, nuban, amount, balance, false)
	}
	paid := func(rows ...string) string {
		return `{"data":[` + strings.Join(rows, ",") + `]}`
	}
	row := func(nuban string, total int64) string {
		return fmt.Sprintf(`{"account_number":%q,"paid_minor":%d}`, nuban, total)
	}
	steps := []struct {
		name     string
		phase    string
		restart  bool
		prep     func()
		method   string
		path     string
		body     string
		key      string
		status   int
		replayed bool
		want     string
	}{
		{name: "payout while active", method: "POST", path: "/v1/closure-payouts", body: payout(a.ada, 100), key: "p0", status: 409, want: apiError("bank_not_closing", "Bank A is active, so it cannot pay out or freeze accounts; that happens only while a bank is closing.")},
		{name: "freeze while active", method: "POST", path: "/v1/accounts/" + a.ada + "/freeze", key: "f0", status: 409, want: apiError("bank_not_closing", "Bank A is active, so it cannot pay out or freeze accounts; that happens only while a bank is closing.")},
		{name: "payout while suspended", phase: config.PhaseSuspended, method: "POST", path: "/v1/closure-payouts", body: payout(a.ada, 100), key: "p0", status: 409, want: apiError("bank_not_closing", "Bank A is suspended, so it cannot pay out or freeze accounts; that happens only while a bank is closing.")},
		{name: "payout without a key", phase: config.PhaseClosing, method: "POST", path: "/v1/closure-payouts", body: payout(a.ada, 100), status: 400, want: apiError("invalid_request", "An Idempotency-Key header of 1 to 255 characters is required on writes.")},
		{name: "payout with a bad body", method: "POST", path: "/v1/closure-payouts", body: `{"account_number":1}`, key: "p1", status: 400, want: apiError("invalid_request", `Send {"account_number":"0123456789","amount_minor":500000}.`)},
		{name: "payout with an unknown field", method: "POST", path: "/v1/closure-payouts", body: `{"account_number":"1","amount":5}`, key: "p1b", status: 400, want: apiError("invalid_request", `Send {"account_number":"0123456789","amount_minor":500000}.`)},
		{name: "payout of zero", method: "POST", path: "/v1/closure-payouts", body: payout(a.ada, 0), key: "p2", status: 422, want: apiError("invalid_request", "The amount must be above zero, got 0.")},
		{name: "payout below zero", method: "POST", path: "/v1/closure-payouts", body: payout(a.ada, -5), key: "p3", status: 422, want: apiError("invalid_request", "The amount must be above zero, got -5.")},
		{name: "payout without an account", method: "POST", path: "/v1/closure-payouts", body: payout("", 5), key: "p4", status: 422, want: apiError("invalid_request", "The account number is empty.")},
		{name: "payout above the balance", method: "POST", path: "/v1/closure-payouts", body: payout(a.ada, 600_000), key: "p5", status: 422, want: apiError("insufficient_funds", fmt.Sprintf("Account %s holds ₦5,000.00 and a payout of ₦6,000.00 was asked for; pay out ₦5,000.00 or less.", a.ada))},
		{name: "the refused payout left the balance", method: "GET", path: "/v1/accounts/" + a.ada, status: 200, want: account(a.ada, "ADA OBI", a.adaID, "active", 500_000)},
		{name: "payout from an unknown account", method: "POST", path: "/v1/closure-payouts", body: payout("0000000000", 5), key: "p6", status: 404, want: apiError("not_found", "No such account.")},
		{name: "first payout", method: "POST", path: "/v1/closure-payouts", body: payout(a.ada, 200_000), key: "p7", status: 201, want: result(a.ada, 200_000, 300_000)},
		{name: "payout replay", method: "POST", path: "/v1/closure-payouts", body: payout(a.ada, 200_000), key: "p7", status: 201, replayed: true, want: result(a.ada, 200_000, 300_000)},
		{name: "payout key reused for another amount", method: "POST", path: "/v1/closure-payouts", body: payout(a.ada, 100_000), key: "p7", status: 422, want: apiError("idempotency_key_reused", "This Idempotency-Key was used with a different request.")},
		{name: "payout replay after a restart", restart: true, method: "POST", path: "/v1/closure-payouts", body: payout(a.ada, 200_000), key: "p7", status: 201, want: strings.Replace(result(a.ada, 200_000, 300_000), `"replayed":false`, `"replayed":true`, 1)},
		{name: "key reused for another amount after a restart", restart: true, method: "POST", path: "/v1/closure-payouts", body: payout(a.ada, 100_000), key: "p7", status: 422, want: apiError("idempotency_key_reused", "That Idempotency-Key was used for a different request.")},
		{name: "second payout takes the rest", method: "POST", path: "/v1/closure-payouts", body: payout(a.ada, 300_000), key: "p8", status: 201, want: result(a.ada, 300_000, 0)},
		{name: "nothing left to pay", method: "POST", path: "/v1/closure-payouts", body: payout(a.ada, 1), key: "p9", status: 422, want: apiError("insufficient_funds", fmt.Sprintf("Account %s holds ₦0.00 and a payout of ₦0.01 was asked for; pay out ₦0.00 or less.", a.ada))},
		{name: "payout to a second account", method: "POST", path: "/v1/closure-payouts", body: payout(a.bola, 100_000), key: "p10", status: 201, want: result(a.bola, 100_000, 0)},
		{name: "totals paid", method: "GET", path: "/v1/closure-payouts", status: 200, want: paid(row(a.ada, 500_000), row(a.bola, 100_000))},
		{name: "first page", method: "GET", path: "/v1/closure-payouts?limit=1", status: 200, want: `{"data":[` + row(a.ada, 500_000) + `],"next_cursor":"` + a.ada + `"}`},
		{name: "second page", method: "GET", path: "/v1/closure-payouts?limit=1&cursor=" + a.ada, status: 200, want: paid(row(a.bola, 100_000))},
		{name: "a cursor that is not a page", method: "GET", path: "/v1/closure-payouts?cursor=nope", status: 400, want: apiError("invalid_cursor", "That cursor is not valid. Use next_cursor from the last page.")},
		{name: "a limit of zero", method: "GET", path: "/v1/closure-payouts?limit=0", status: 422, want: apiError("invalid_request", "Limit must be a number from 1 to 1000.")},
		{name: "freeze", method: "POST", path: "/v1/accounts/" + a.ada + "/freeze", key: "f1", status: 200, want: account(a.ada, "ADA OBI", a.adaID, "frozen", 0)},
		{name: "freeze replay", method: "POST", path: "/v1/accounts/" + a.ada + "/freeze", key: "f1", status: 200, replayed: true, want: account(a.ada, "ADA OBI", a.adaID, "frozen", 0)},
		{name: "freeze again with a new key", method: "POST", path: "/v1/accounts/" + a.ada + "/freeze", key: "f2", status: 200, want: account(a.ada, "ADA OBI", a.adaID, "frozen", 0)},
		{name: "freeze without a key", method: "POST", path: "/v1/accounts/" + a.ada + "/freeze", status: 400, want: apiError("invalid_request", "An Idempotency-Key header of 1 to 255 characters is required on writes.")},
		{name: "freeze an unknown account", method: "POST", path: "/v1/accounts/0000000000/freeze", key: "f3", status: 404, want: apiError("not_found", "No such account.")},
		{name: "a frozen account stops paying out", method: "POST", path: "/v1/closure-payouts", body: payout(a.ada, 1), key: "p11", status: 422, want: apiError("account_restricted", "Account is frozen.")},
		{name: "freeze a closed account", prep: func() {
			if err := a.books.CloseAccount(context.Background(), a.bola, adminNow); err != nil {
				t.Fatal(err)
			}
		}, method: "POST", path: "/v1/accounts/" + a.bola + "/freeze", key: "f5", status: 422, want: apiError("account_restricted", "Account is closed.")},
		{name: "the bank closes", phase: config.PhaseClosed, method: "POST", path: "/v1/accounts/" + a.bola + "/freeze", key: "f4", status: 409, want: apiError("bank_not_closing", "Bank A is closed, so it cannot pay out or freeze accounts; that happens only while a bank is closing.")},
		{name: "totals can still be read", method: "GET", path: "/v1/closure-payouts", status: 200, want: paid(row(a.ada, 500_000), row(a.bola, 100_000))},
	}
	for _, s := range steps {
		if s.phase != "" {
			a.phase(s.phase)
		}
		if s.restart {
			a.restart(config.PhaseClosing)
		}
		if s.prep != nil {
			s.prep()
		}
		got := a.call(s.method, s.path, s.body, s.key)
		if got.status != s.status || got.replayed != s.replayed || got.body != s.want {
			t.Fatalf("%s: got %d replayed=%v %s\nwant %d replayed=%v %s", s.name, got.status, got.replayed, got.body, s.status, s.replayed, s.want)
		}
	}
}

func TestBookRoutes(t *testing.T) {
	a := newAdminBank(t)
	ctx := context.Background()
	sid := banktest.Session("000015", 7)
	credit := bank.NIPCredit{SessionID: sid, FromCode: "000015", FromBankName: "Bank B", FromNUBAN: "9020000011", FromName: "CHIDI OKAFOR", ToNUBAN: a.ada, Amount: 250_000, Narration: "Rent", CreatedAt: adminNow}
	if ans, err := a.books.Credit(ctx, credit, adminNow); err != nil || ans.Code != "00" {
		t.Fatalf("credit: %+v, %v", ans, err)
	}
	if _, err := a.books.CreateCustomer(ctx, "cus-ada-2", "ADA OBI", "+2348030000001", clock.Date{Y: 1990, M: 1, D: 1}, adminBVN, "", "", adminNow); err != nil {
		t.Fatal(err)
	}
	lines, _, err := a.books.Recent(ctx, a.ada, 10, "")
	if err != nil || len(lines) != 2 {
		t.Fatalf("lines %v, %v", lines, err)
	}
	customers, _, err := a.books.Customers(ctx, 10, "")
	if err != nil || len(customers) != 3 {
		t.Fatalf("customers %v, %v", customers, err)
	}
	customer := func(c bank.Customer, username string) string {
		return fmt.Sprintf(`{"id":%q,"name":%q,"phone":%q,"bvn":%q,"status":"active",%s"dob":%q,"created_at":%q}`, c.ID, c.Name, c.Phone, c.BVN, username, c.DOB.String(), adminNow.Local().Format(time.RFC3339Nano))
	}
	entry := func(l bank.FeedLine, rest string) string {
		return fmt.Sprintf(`{"id":%q,"account":%q,"seq":%d,"amount_minor":%d,"balance_after_minor":%d,"posted_at":%q,"value_date":%q,"narration":%q,%s"reference":%q%s}`, l.Key, l.NUBAN, l.Seq, l.Amount, l.BalanceAfter, adminNow.Format(time.RFC3339Nano), clock.DateOf(adminNow).String(), l.Narration, rest, l.BankRef, "")
	}
	deposit, inward := lines[1], lines[0]
	depositEntry := entry(deposit, `"channel":"branch",`)
	inwardEntry := strings.TrimSuffix(entry(inward, `"channel":"nip","session_id":"`+sid+`",`), "}") + `,"counterparty":{"name":"CHIDI OKAFOR","account_number":"9020000011","bank_code":"000015","bank_name":"Bank B"}}`
	notReady := apiError("unavailable", "The bank database is not ready yet. Try again soon.")
	rows := []struct {
		name   string
		method string
		path   string
		body   string
		status int
		want   string
	}{
		{"read an account", "GET", "/v1/accounts/" + a.ada, "", 200, account(a.ada, "ADA OBI", a.adaID, "active", 750_000)},
		{"read an unknown account", "GET", "/v1/accounts/0000000000", "", 404, apiError("not_found", "No such account.")},
		{"customers by BVN", "GET", "/v1/customers?bvn=" + otherBVN, "", 200, `{"data":[` + customer(customers[1], ``) + `]}`},
		{"customers by BVN with a username", "GET", "/v1/customers?bvn=" + adminBVN + "&limit=1", "", 200, `{"data":[` + customer(customers[0], `"username":"ada",`) + `],"next_cursor":"` + customers[0].ID + `"}`},
		{"customers by BVN, next page", "GET", "/v1/customers?bvn=" + adminBVN + "&limit=1&cursor=" + customers[0].ID, "", 200, `{"data":[` + customer(customers[2], ``) + `]}`},
		{"customers by a BVN nobody has", "GET", "/v1/customers?bvn=22900000000", "", 200, `{"data":[]}`},
		{"a BVN with too few digits", "GET", "/v1/customers?bvn=229123", "", 400, apiError("invalid_request", "bvn must be 11 digits.")},
		{"a BVN with a letter", "GET", "/v1/customers?bvn=2291234567a", "", 400, apiError("invalid_request", "bvn must be 11 digits.")},
		{"an empty BVN", "GET", "/v1/customers?bvn=", "", 400, apiError("invalid_request", "bvn must be 11 digits.")},
		{"totals", "GET", "/v1/books/totals", "", 200, `{"customers":3,"accounts":2,"deposits_minor":850000}`},
		{"entries newest first", "GET", "/v1/books/accounts/" + a.ada + "/entries", "", 200, `{"data":[` + inwardEntry + `,` + depositEntry + `]}`},
		{"entries first page", "GET", "/v1/books/accounts/" + a.ada + "/entries?limit=1", "", 200, `{"data":[` + inwardEntry + `],"next_cursor":"` + fmt.Sprint(inward.Seq) + `"}`},
		{"entries next page", "GET", "/v1/books/accounts/" + a.ada + "/entries?limit=1&cursor=" + fmt.Sprint(inward.Seq), "", 200, `{"data":[` + depositEntry + `]}`},
		{"entries with a bad cursor", "GET", "/v1/books/accounts/" + a.ada + "/entries?cursor=x", "", 400, apiError("invalid_cursor", "That cursor is not valid. Use next_cursor from the last page.")},
		{"entries with a limit of zero", "GET", "/v1/books/accounts/" + a.ada + "/entries?limit=0", "", 422, apiError("invalid_request", "Limit must be a number from 1 to 1000.")},
		{"entries of an unknown account", "GET", "/v1/books/accounts/0000000000/entries", "", 404, apiError("not_found", "No such account.")},
		{"one entry", "GET", "/v1/books/accounts/" + a.ada + "/entries/" + inward.Key, "", 200, inwardEntry},
		{"an entry of another account", "GET", "/v1/books/accounts/" + a.bola + "/entries/" + inward.Key, "", 404, apiError("not_found", "No such entry.")},
		{"an entry that is not a number", "GET", "/v1/books/accounts/" + a.ada + "/entries/abc", "", 404, apiError("not_found", "No such entry.")},
		{"an entry of an unknown account", "GET", "/v1/books/accounts/0000000000/entries/1", "", 404, apiError("not_found", "No such account.")},
		{"entries of a session", "GET", "/v1/books/sessions/" + sid, "", 200, `{"data":[` + inwardEntry + `]}`},
		{"a session the bank never saw", "GET", "/v1/books/sessions/" + banktest.Session("000015", 8), "", 200, `{"data":[]}`},
		{"a session ID of letters", "GET", "/v1/books/sessions/abc", "", 400, apiError("invalid_request", "session_id must have 30 digits.")},
		{"the ledger check", "GET", "/v1/ledger/check", "", 200, `{"ok":true,"trial_balance_minor":0,"balance_mismatches":[],"event_mismatches":[],"unbalanced_entries":[]}`},
	}
	for _, r := range rows {
		got := a.call(r.method, r.path, r.body, "")
		if got.status != r.status || got.replayed || got.body != r.want {
			t.Fatalf("%s: got %d %s\nwant %d %s", r.name, got.status, got.body, r.status, r.want)
		}
	}
	if strings.Contains(a.logs.String(), adminPassword) || strings.Contains(a.logs.String(), "wrong-password-here") {
		t.Fatalf("a password reached the log: %s", a.logs)
	}
	a.srv = New(Config{Bank: config.Bank{ID: "bank-a", Name: "Bank A"}, Link: control.Link{Token: adminToken}, Log: slog.New(slog.NewTextHandler(a.logs, nil)), Clock: clock.New(), Phase: config.PhaseProvisioning})
	for _, path := range []string{"/v1/books/totals", "/v1/ledger/check", "/v1/accounts/" + a.ada, "/v1/customers", "/v1/closure-payouts", "/v1/nip/sends/open"} {
		if got := a.call("GET", path, "", ""); got.status != 503 || got.body != notReady {
			t.Fatalf("%s before the books open: got %d %s", path, got.status, got.body)
		}
	}
	for _, path := range []string{"/v1/closure-payouts", "/v1/accounts/" + a.ada + "/freeze"} {
		if got := a.call("POST", path, `{"account_number":"`+a.ada+`","amount_minor":5}`, "k1"); got.status != 503 || got.body != notReady {
			t.Fatalf("POST %s before the books open: got %d %s", path, got.status, got.body)
		}
	}
	a.srv.SetBooks(a.books)
	if got := a.call("GET", "/v1/books/totals", "", ""); got.status != 200 || got.body != `{"customers":3,"accounts":2,"deposits_minor":850000}` {
		t.Fatalf("totals after the books open: got %d %s", got.status, got.body)
	}
}

func TestParallelPayoutsCannotOverdrawTheAccount(t *testing.T) {
	a := newAdminBank(t)
	a.phase(config.PhaseClosing)
	const tries = 8
	replies := make(chan adminReply, tries)
	for i := range tries {
		go func() {
			replies <- a.call("POST", "/v1/closure-payouts", fmt.Sprintf(`{"account_number":%q,"amount_minor":300000}`, a.ada), fmt.Sprintf("par-%d", i))
		}()
	}
	created := 0
	refused := apiError("insufficient_funds", fmt.Sprintf("Account %s holds ₦2,000.00 and a payout of ₦3,000.00 was asked for; pay out ₦2,000.00 or less.", a.ada))
	for range tries {
		switch got := <-replies; {
		case got.status == 201:
			created++
		case got.status != 422 || got.body != refused:
			t.Fatalf("a losing payout: got %d %s, want 422 %s", got.status, got.body, refused)
		}
	}
	if created != 1 {
		t.Fatalf("%d payouts were posted, want 1", created)
	}
	if got := a.call("GET", "/v1/accounts/"+a.ada, "", ""); got.body != account(a.ada, "ADA OBI", a.adaID, "active", 200_000) {
		t.Fatalf("balance after the race: %s", got.body)
	}
}
