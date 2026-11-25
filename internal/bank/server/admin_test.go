package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/banktest"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

const (
	adminToken = "control-token"
	adminBVN   = "22912345678"
)

var (
	adminNow     = clock.New().Now()
	requestIDRef = regexp.MustCompile(`"request_id":"req_[a-z0-9]+"`)
	dob          = clock.Date{Y: 1990, M: 1, D: 1}
)

type adminBank struct {
	t          *testing.T
	logs       *strings.Builder
	books      *bank.Bank
	srv        *Server
	ada, bola  string
	adaID, bID string
}

type adminReply struct {
	status   int
	replayed bool
	body     string
}

func chk(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func hashOf(t testing.TB, password string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	chk(t, err)
	return string(h)
}

func newAdminBank(t *testing.T) *adminBank {
	t.Helper()
	ctx := context.Background()
	a := &adminBank{t: t, logs: &strings.Builder{}}
	a.books = bank.New(ledger.New(banktest.Schema(t), nil), bank.Profile{ID: "bank-a", Name: "Bank A", NIPCode: "000014", NUBANCode: "014", PasswordCost: bcrypt.MinCost}, banktest.Fees(t))
	chk(t, a.books.Setup(ctx))
	chk(t, a.books.Genesis(ctx, 1_000_000_000_000, 200_000_000_000, adminNow))
	ada, err := a.books.CreateCustomer(ctx, "cus-ada", "ADA OBI", "+2348030000001", dob, adminBVN, "ada", hashOf(t, "pw"), adminNow)
	chk(t, err)
	bola, err := a.books.CreateCustomer(ctx, "cus-bola", "BOLA ADE", "+2348030000002", dob, "22987654321", "", "", adminNow)
	chk(t, err)
	a.adaID, a.bID = ada.ID, bola.ID
	a.ada, a.bola = a.open(ada.ID, "acc-ada", 500_000), a.open(bola.ID, "acc-bola", 100_000)
	a.restart(config.PhaseActive)
	return a
}

func (a *adminBank) open(customer, key string, deposit int64) string {
	acc, err := a.books.OpenAccount(context.Background(), key, customer, bank.ProductCurrent, adminNow)
	chk(a.t, err)
	_, err = a.books.CashDeposit(context.Background(), "dep-"+key, acc.NUBAN, deposit, adminNow)
	chk(a.t, err)
	return acc.NUBAN
}

func (a *adminBank) restart(phase string) {
	a.srv = New(Config{Bank: config.Bank{ID: "bank-a", Name: "Bank A", NIPCode: "000014", NUBANCode: "014"}, Books: a.books, Fees: banktest.Fees(a.t), Link: control.Link{Token: adminToken}, Log: slog.New(slog.NewTextHandler(a.logs, nil)), Clock: clock.New(), Phase: phase})
}

func (a *adminBank) phase(phase string) {
	a.srv.lifeMu.Lock()
	defer a.srv.lifeMu.Unlock()
	a.srv.phase = phase
}

func (a *adminBank) call(method, path, body, key string) adminReply {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:1"
	r.Header.Set("Authorization", "Bearer "+adminToken)
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	a.srv.API().ServeHTTP(w, r)
	return adminReply{w.Code, w.Header().Get("Idempotent-Replayed") == "true", requestIDRef.ReplaceAllString(strings.TrimSpace(w.Body.String()), `"request_id":"req_ID"`)}
}

func apiError(code, message string) string {
	return fmt.Sprintf(`{"error":{"code":%q,"message":%q,"request_id":"req_ID"}}`, code, message)
}

func TestClosureRoutes(t *testing.T) {
	t.Parallel()
	a := newAdminBank(t)
	notClosing := func(state string) string {
		return apiError("bank_not_closing", "Bank A is "+state+", so it cannot pay out or freeze accounts; that happens only while a bank is closing.")
	}
	const send = `Send {"account_number":"0123456789","amount_minor":500000}.`
	payout := func(nuban string, amount int64) string {
		return fmt.Sprintf(`{"account_number":%q,"amount_minor":%d}`, nuban, amount)
	}
	short := func(held, asked string) string {
		return apiError("insufficient_funds", fmt.Sprintf("Account %s holds %s and a payout of %s was asked for; pay out %s or less.", a.ada, held, asked, held))
	}
	post, freeze, list := "/v1/closure-payouts", "/v1/accounts/"+a.ada+"/freeze", "/v1/closure-payouts"
	steps := []struct {
		name, phase, method, path, body, key string
		restart, replayed                    bool
		status                               int
		want                                 string
	}{
		{name: "payout while active", method: "POST", path: post, body: payout(a.ada, 100), key: "p0", status: 409, want: notClosing("active")},
		{name: "freeze while suspended", phase: config.PhaseSuspended, method: "POST", path: freeze, key: "f0", status: 409, want: notClosing("suspended")},
		{name: "payout without a key", phase: config.PhaseClosing, method: "POST", path: post, body: payout(a.ada, 100), status: 400, want: apiError("invalid_request", "An Idempotency-Key header of 1 to 255 characters is required on writes.")},
		{name: "payout with a bad body", method: "POST", path: post, body: `{"account_number":1}`, key: "p1", status: 400, want: apiError("invalid_request", send)},
		{name: "payout of zero", method: "POST", path: post, body: payout(a.ada, 0), key: "p2", status: 422, want: apiError("invalid_request", "The amount must be above zero, got 0.")},
		{name: "payout without an account", method: "POST", path: post, body: payout("", 5), key: "p4", status: 422, want: apiError("invalid_request", "The account number is empty.")},
		{name: "payout above the balance", method: "POST", path: post, body: payout(a.ada, 600_000), key: "p5", status: 422, want: short("₦5,000.00", "₦6,000.00")},
		{name: "payout from an unknown account", method: "POST", path: post, body: payout("0000000000", 5), key: "p6", status: 404, want: apiError("not_found", "No such account.")},
		{name: "first payout", method: "POST", path: post, body: payout(a.ada, 200_000), key: "p7", status: 201, want: `"balance_minor":300000,"replayed":false`},
		{name: "payout replay", method: "POST", path: post, body: payout(a.ada, 200_000), key: "p7", status: 201, replayed: true, want: `"balance_minor":300000,"replayed":false`},
		{name: "key reused for another amount", method: "POST", path: post, body: payout(a.ada, 100_000), key: "p7", status: 422, want: apiError("idempotency_key_reused", "This Idempotency-Key was used with a different request.")},
		{name: "replay after a restart", restart: true, method: "POST", path: post, body: payout(a.ada, 200_000), key: "p7", status: 201, want: `"balance_minor":300000,"replayed":true`},
		{name: "key reused after a restart", restart: true, method: "POST", path: post, body: payout(a.ada, 100_000), key: "p7", status: 422, want: apiError("idempotency_key_reused", "That Idempotency-Key was used for a different request.")},
		{name: "second payout takes the rest", method: "POST", path: post, body: payout(a.ada, 300_000), key: "p8", status: 201, want: `"balance_minor":0,"replayed":false`},
		{name: "nothing left to pay", method: "POST", path: post, body: payout(a.ada, 1), key: "p9", status: 422, want: short("₦0.00", "₦0.01")},
		{name: "payout to a second account", method: "POST", path: post, body: payout(a.bola, 100_000), key: "p10", status: 201, want: `"balance_minor":0`},
		{name: "totals paid", method: "GET", path: list, status: 200, want: `{"data":[{"account_number":"` + a.ada + `","paid_minor":500000},{"account_number":"` + a.bola + `","paid_minor":100000}]}`},
		{name: "first page", method: "GET", path: list + "?limit=1", status: 200, want: `"next_cursor":"` + a.ada + `"`},
		{name: "bad cursor", method: "GET", path: list + "?cursor=nope", status: 400, want: apiError("invalid_cursor", "That cursor is not valid. Use next_cursor from the last page.")},
		{name: "limit of zero", method: "GET", path: list + "?limit=0", status: 422, want: apiError("invalid_request", "Limit must be a number from 1 to 1000.")},
		{name: "freeze", method: "POST", path: freeze, key: "f1", status: 200, want: `"status":"frozen"`},
		{name: "freeze unknown", method: "POST", path: "/v1/accounts/0000000000/freeze", key: "f3", status: 404, want: apiError("not_found", "No such account.")},
		{name: "a frozen account stops paying out", method: "POST", path: post, body: payout(a.ada, 1), key: "p11", status: 422, want: apiError("account_restricted", "Account is frozen.")},
		{name: "the bank closes", phase: config.PhaseClosed, method: "POST", path: freeze, key: "f4", status: 409, want: notClosing("closed")},
	}
	for _, s := range steps {
		if s.phase != "" {
			a.phase(s.phase)
		}
		if s.restart {
			a.restart(config.PhaseClosing)
		}
		got := a.call(s.method, s.path, s.body, s.key)
		if got.status != s.status || got.replayed != s.replayed || !strings.Contains(got.body, s.want) {
			t.Fatalf("%s: got %d %s, want %d %s", s.name, got.status, got.body, s.status, s.want)
		}
	}
}

func TestBookRoutes(t *testing.T) {
	t.Parallel()
	a := newAdminBank(t)
	ctx := context.Background()
	sid := banktest.Session("000015", 7)
	_, err := a.books.Credit(ctx, bank.NIPCredit{SessionID: sid, FromCode: "000015", FromBankName: "Bank B", FromNUBAN: "9020000011", FromName: "CHIDI OKAFOR", ToNUBAN: a.ada, Amount: 250_000, CreatedAt: adminNow}, adminNow)
	chk(t, err)
	_, err = a.books.CreateCustomer(ctx, "cus-ada-2", "ADA OBI", "+2348030000001", dob, adminBVN, "", "", adminNow)
	chk(t, err)
	lines, _, err := a.books.Recent(ctx, a.ada, 10, "")
	chk(t, err)
	inward := lines[0]
	badBVN := apiError("invalid_request", "bvn must be 11 digits.")
	badCursor := apiError("invalid_cursor", "That cursor is not valid. Use next_cursor from the last page.")
	entries := "/v1/books/accounts/" + a.ada + "/entries"
	rows := []struct {
		name, path string
		status     int
		want       string
	}{
		{"read an account", "/v1/accounts/" + a.ada, 200, `"balance_minor":750000`},
		{"unknown account", "/v1/accounts/0000000000", 404, apiError("not_found", "No such account.")},
		{"customers by BVN with a username", "/v1/customers?bvn=" + adminBVN + "&limit=1", 200, `"username":"ada"`},
		{"customers by a BVN nobody has", "/v1/customers?bvn=22900000000", 200, `{"data":[]}`},
		{"a BVN with too few digits", "/v1/customers?bvn=229123", 400, badBVN},
		{"a BVN with a letter", "/v1/customers?bvn=2291234567a", 400, badBVN},
		{"totals", "/v1/books/totals", 200, `{"customers":3,"accounts":2,"deposits_minor":850000}`},
		{"entries newest first", entries, 200, `"channel":"nip","session_id":"` + sid + `"`},
		{"entries first page", entries + "?limit=1", 200, `"next_cursor":"` + fmt.Sprint(inward.Seq) + `"`},
		{"entries bad cursor", entries + "?cursor=x", 400, badCursor},
		{"entries limit of zero", entries + "?limit=0", 422, apiError("invalid_request", "Limit must be a number from 1 to 1000.")},
		{"entries of an unknown account", "/v1/books/accounts/0000000000/entries", 404, apiError("not_found", "No such account.")},
		{"one entry", entries + "/" + inward.Key, 200, `"counterparty":{"name":"CHIDI OKAFOR"`},
		{"an entry of another account", "/v1/books/accounts/" + a.bola + "/entries/" + inward.Key, 404, apiError("not_found", "No such entry.")},
		{"entries of a session", "/v1/books/sessions/" + sid, 200, `"session_id":"` + sid + `"`},
		{"a session ID of letters", "/v1/books/sessions/abc", 400, apiError("invalid_request", "session_id must have 30 digits.")},
		{"the ledger check", "/v1/ledger/check", 200, `{"ok":true,`},
	}
	for _, r := range rows {
		if got := a.call("GET", r.path, "", ""); got.status != r.status || !strings.Contains(got.body, r.want) {
			t.Fatalf("%s: got %d %s, want %d %s", r.name, got.status, got.body, r.status, r.want)
		}
	}
	notReady := apiError("unavailable", "The bank database is not ready yet. Try again soon.")
	a.srv = New(Config{Bank: config.Bank{ID: "bank-a", Name: "Bank A"}, Link: control.Link{Token: adminToken}, Log: slog.New(slog.NewTextHandler(a.logs, nil)), Clock: clock.New(), Phase: config.PhaseProvisioning})
	for _, path := range []string{"/v1/books/totals", "/v1/ledger/check", "/v1/accounts/" + a.ada, "/v1/customers", "/v1/closure-payouts", "/v1/nip/sends/open"} {
		if got := a.call("GET", path, "", ""); got.status != 503 || got.body != notReady {
			t.Fatalf("%s before the books open: got %d %s", path, got.status, got.body)
		}
	}
	if got := a.call("POST", "/v1/closure-payouts", `{"account_number":"`+a.ada+`","amount_minor":5}`, "k1"); got.status != 503 || got.body != notReady {
		t.Fatalf("payout before the books open: got %d %s", got.status, got.body)
	}
	a.srv.SetBooks(a.books)
	if got := a.call("GET", "/v1/books/totals", "", ""); got.status != 200 {
		t.Fatalf("totals after the books open: got %d %s", got.status, got.body)
	}
}

func TestParallelPayoutsCannotOverdrawTheAccount(t *testing.T) {
	t.Parallel()
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
	if got := a.call("GET", "/v1/accounts/"+a.ada, "", ""); created != 1 || !strings.Contains(got.body, `"balance_minor":200000`) {
		t.Fatalf("%d payouts posted, balance %s, want 1 and 200000", created, got.body)
	}
}

func TestCustomerUsernamesAreMapped(t *testing.T) {
	t.Parallel()
	a := newAdminBank(t)
	ctx := context.Background()
	hash := hashOf(t, "pw")
	_, noPassword := a.books.CreateCustomer(ctx, "cus-np", "NO PASS", "+2348030000003", dob, "22911111111", "nopass", "", adminNow)
	_, taken := a.books.CreateCustomer(ctx, "cus-tk", "TWIN", "+2348030000004", dob, "22922222222", "ada", hash, adminNow)
	for _, r := range []struct {
		err    error
		status int
		want   string
	}{
		{noPassword, 422, apiError("invalid_request", `Username "nopass" needs a password.`)},
		{taken, 409, apiError("username_taken", "Username is taken: ada.")},
	} {
		w := httptest.NewRecorder()
		WriteBankingError(w, httptest.NewRequest("POST", "/v1/customers", nil), slog.New(slog.NewTextHandler(a.logs, nil)), func() bool { return false }, r.err)
		if body := strings.Replace(strings.TrimSpace(w.Body.String()), `"request_id":""`, `"request_id":"req_ID"`, 1); w.Code != r.status || body != r.want {
			t.Errorf("%d %s, want %d %s", w.Code, body, r.status, r.want)
		}
	}
	created, err := a.books.CreateCustomer(ctx, "cus-un", "BO", "+2348030000005", dob, "22933333333", "bo", hash, adminNow)
	chk(t, err)
	w := httptest.NewRecorder()
	web.WriteJSON(w, 201, created)
	if !strings.Contains(w.Body.String(), `"username":"bo"`) || strings.Contains(w.Body.String(), "password") {
		t.Errorf("customer response %s", w.Body)
	}
}
