package core

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	validator "github.com/pb33f/libopenapi-validator"

	"github.com/evolutionlabs-core/beerus/internal/bank/participant"
	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/state"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

var (
	stampedField = regexp.MustCompile(`"(created_at|posted_at|bvn)":"[^"]*"`)
	accountNo    = regexp.MustCompile(`"nuban":"(\d{10})"`)
)

const reloads = 5

type check struct {
	name string
	got  func() (string, error)
	want string
}

func runChecks(t *testing.T, rows []check) {
	t.Helper()
	for _, r := range rows {
		got, err := r.got()
		if err != nil {
			got = err.Error()
		}
		if got != r.want {
			t.Errorf("%s: got %q, want %q", r.name, got, r.want)
		}
	}
}

func serveAt(t *testing.T, addr string, h http.Handler) string {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: probeLimit}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ln)
	}()
	t.Cleanup(func() {
		_ = srv.Close()
		<-done
	})
	return "http://" + ln.Addr().String()
}

func bankToken(t *testing.T, core *App, id string) string {
	t.Helper()
	tok, err := config.ReadSecret(filepath.Join(config.BankDir(core.cfg.Dir, id), config.BankTokenFile))
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func bankOn(t *testing.T, core *App, coreURL, id string, ensure bool) *participant.Participant {
	t.Helper()
	p, err := participant.New(t.Context(), core.cfg.Dir, participant.Options{Logger: core.log, BankID: id, CoreURL: coreURL, Bind: "127.0.0.1", DatabaseURL: core.cfg.DatabaseURL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	if ensure {
		p.Ensure(t.Context())
	}
	return p
}

func adminOn(t *testing.T, core *App, p *participant.Participant, id string) *bankAdmin {
	t.Helper()
	c := &bankAdmin{link: control.NewLink(bankToken(t, core, id), ""), id: id, name: core.cfg.Bank(id).Name, base: serveAt(t, "127.0.0.1:0", p.Server().API())}
	phase := control.PhaseRequest{Phase: PhaseActive, Generation: 1, WorldID: "wld_1"}
	if err := c.call(t.Context(), http.MethodPut, "/v1/phase", "", phase, nil); err != nil {
		t.Fatal(err)
	}
	return c
}

func setBankPhase(a *App, id, phase string) {
	a.lifeMu.Lock()
	defer a.lifeMu.Unlock()
	a.status[id].Phase = phase
}

func TestBanks(t *testing.T) {
	core, stopNIBSS := newTestAppWithNIBSS(t, Options{Logger: slog.New(slog.DiscardHandler)})
	coreURL := startCore(t, core)
	t.Run("books not ready", func(t *testing.T) {
		c := adminOn(t, core, bankOn(t, core, coreURL, "bank-a", false), "bank-a")
		_, err := c.totals(t.Context())
		var down *bankDownError
		if !errors.As(err, &down) || down.Name != "Bank A" || err.Error() != "bank-a is not answering: The bank database is not ready yet. Try again soon." {
			t.Fatalf("got %v, want a down error for Bank A", err)
		}
	})
	t.Run("admin", func(t *testing.T) {
		bankAdminRows(t, adminOn(t, core, bankOn(t, core, coreURL, "bank-a", true), "bank-a"))
	})
	t.Run("retire", func(t *testing.T) { retireRows(t, core, coreURL) })
	t.Run("verify", func(t *testing.T) { verifyRows(t, core) })
	v := participantValidator(t)
	t.Run("participant control contract", func(t *testing.T) { controlContract(t, v) })
	t.Run("participant bank contract", func(t *testing.T) {
		bankContract(t, v, core, bankOn(t, core, coreURL, "bank-b", true), "bank-b", stopNIBSS)
	})
}

func startCore(t *testing.T, core *App) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	core.cfg.Connect.ControlPort = ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: core.ControlHandler(), ReadHeaderTimeout: probeLimit}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ln)
	}()
	t.Cleanup(func() {
		_ = srv.Close()
		<-done
	})
	return "http://" + ln.Addr().String()
}

func TestBankAdminWhenTheBankIsAway(t *testing.T) {
	a := &App{cfg: &config.Config{Banks: []*config.Bank{{ID: "bank-a", Name: "Bank A", Port: 1}}}, link: control.NewLink("", ""), log: slog.New(slog.DiscardHandler)}
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
	a.bankError(rec, httptest.NewRequest(http.MethodGet, "/banks/bank-a/close-impact", nil), err)
	want := `{"error":{"code":"unavailable","message":"Bank A is not running or cannot be reached. Run beerus get banks to see its state, then try again.","request_id":""}}`
	if rec.Code != http.StatusServiceUnavailable || rec.Body.String() != want {
		t.Fatalf("got %d %q, want 503 %q", rec.Code, rec.Body.String(), want)
	}
}

func bankAdminRows(t *testing.T, c *bankAdmin) {
	ctx := t.Context()
	body := control.CustomerRequest{Name: "Ada Obi", Phone: "08031234567", DOB: "1990-04-21"}
	var made Customer
	if err := c.call(ctx, http.MethodPost, bankCustomers, "cus-ada", body, &made); err != nil {
		t.Fatal(err)
	}
	opened, err := c.openAccount(ctx, "acc-ada", made.ID, "savings")
	if err != nil {
		t.Fatal(err)
	}
	cash := server.CashRequest{Account: opened.NUBAN, Amount: "5,000.00"}
	if err := c.call(ctx, http.MethodPost, "/v1/deposits", "cash-1", cash, nil); err != nil {
		t.Fatal(err)
	}
	line, _, err := c.recent(ctx, opened.NUBAN, 50, "")
	if err != nil || len(line) != 1 {
		t.Fatalf("recent: %v, %v", line, err)
	}
	bola := Customer{Name: "Bola Ade", Phone: "08039876543", DOB: made.DOB}
	var bolaID string
	rows := []check{
		{"state", func() (string, error) {
			s, err := c.state(ctx)
			ids := ""
			for _, st := range s.Steps {
				if st.Done {
					ids += " " + st.ID
				}
			}
			return s.ParticipantID + " " + s.Phase + ids, err
		}, "bank-a active allocate directory database ledger_schema openbanking_schema files cbn site api"},
		{"customers", func() (string, error) { v, err := c.customers(ctx); return fmt.Sprint(len(v), " ", v[0].Name), err }, "1 Ada Obi"},
		{"customers by BVN", func() (string, error) { v, err := c.customersByBVN(ctx, made.BVN); return fmt.Sprint(len(v)), err }, "1"},
		{"customers by unknown BVN", func() (string, error) { v, err := c.customersByBVN(ctx, "22900000000"); return fmt.Sprint(len(v)), err }, "0"},
		{"create customer", func() (string, error) {
			v, err := c.createCustomer(ctx, "cus-bola", bola)
			bolaID = v.ID
			return v.Name, err
		}, "Bola Ade"},
		{"create customer again with the same key", func() (string, error) {
			v, err := c.createCustomer(ctx, "cus-bola", bola)
			return fmt.Sprint(v.ID == bolaID), err
		}, "true"},
		{"create another customer with the same key", func() (string, error) {
			_, err := c.createCustomer(ctx, "cus-bola", Customer{Name: "Chi Eze", Phone: "08030000002", DOB: made.DOB})
			return "", err
		}, "This Idempotency-Key was used with a different request."},
		{"account", func() (string, error) {
			v, err := c.account(ctx, opened.NUBAN)
			return fmt.Sprint(v.Name, " ", v.Status, " ", v.Balance), err
		}, "Ada Obi active 500000"},
		{"unknown account", func() (string, error) { _, err := c.account(ctx, "0000000000"); return "", err }, "ledger: unknown account: 0000000000"},
		{"open account again with the same key", func() (string, error) {
			v, err := c.openAccount(ctx, "acc-ada", made.ID, "savings")
			return v.NUBAN, err
		}, opened.NUBAN},
		{"accounts of a customer", func() (string, error) {
			v, next, err := c.accounts(ctx, made.ID, 10, "")
			return fmt.Sprint(len(v), " ", next == ""), err
		}, "1 true"},
		{"accounts with a bad cursor", func() (string, error) { _, _, err := c.accounts(ctx, "", 10, "nope"); return "", err }, `bank: invalid cursor: "nope"`},
		{"totals", func() (string, error) { v, err := c.totals(ctx); return fmt.Sprint(v), err }, "{2 1 500000}"},
		{"check", func() (string, error) { v, err := c.check(ctx); return v.String(), err }, "trial balance 0, balance mismatches [], event mismatches [], unbalanced entries []"},
		{"entry", func() (string, error) {
			v, err := c.line(ctx, opened.NUBAN, line[0].Key)
			return fmt.Sprint(v.Amount, " ", v.BalanceAfter, " ", v.Narration), err
		}, "500000 500000 CASH DEPOSIT"},
		{"unknown entry", func() (string, error) { _, err := c.line(ctx, opened.NUBAN, "ln_none"); return "", err }, "bank: unknown statement line: ln_none"},
		{"entries with a bad cursor", func() (string, error) { _, _, err := c.recent(ctx, opened.NUBAN, 10, "nope"); return "", err }, `bank: invalid cursor: "nope"`},
		{"payout while active", func() (string, error) { return "", c.payout(ctx, "close:bank-a:"+opened.NUBAN, opened.NUBAN, 100) }, "Bank A is active, so it cannot pay out or freeze accounts; that happens only while a bank is closing."},
		{"freeze while active", func() (string, error) { return "", c.freeze(ctx, "close:bank-a:"+opened.NUBAN+":freeze", opened.NUBAN) }, "Bank A is active, so it cannot pay out or freeze accounts; that happens only while a bank is closing."},
	}
	runChecks(t, rows)
	closing := control.PhaseRequest{Phase: PhaseClosing, Generation: 2, WorldID: "wld_1"}
	if err := c.call(ctx, http.MethodPut, "/v1/phase", "", closing, nil); err != nil {
		t.Fatal(err)
	}
	key := "closing:" + opened.NUBAN
	rows = []check{
		{"payout", func() (string, error) { return "", c.payout(ctx, key, opened.NUBAN, 120000) }, ""},
		{"payout again with the same key", func() (string, error) { return "", c.payout(ctx, key, opened.NUBAN, 120000) }, ""},
		{"payouts", func() (string, error) { v, err := c.payouts(ctx); return fmt.Sprint(v[opened.NUBAN]), err }, "120000"},
		{"balance", func() (string, error) { v, err := c.account(ctx, opened.NUBAN); return fmt.Sprint(v.Balance), err }, "380000"},
		{"payout with the same key and another amount", func() (string, error) { return "", c.payout(ctx, key, opened.NUBAN, 5) }, "This Idempotency-Key was used with a different request."},
		{"freeze", func() (string, error) { return "", c.freeze(ctx, key+":freeze", opened.NUBAN) }, ""},
		{"status after the freeze", func() (string, error) { v, err := c.account(ctx, opened.NUBAN); return v.Status, err }, "frozen"},
	}
	runChecks(t, rows)
}

func retireRows(t *testing.T, a *App, coreURL string) {
	b := a.cfg.Bank("bank-a")
	ctx := t.Context()
	if a.bankRetired(b.ID) {
		t.Fatal("a bank counted as retired before it was")
	}
	setBankPhase(a, b.ID, PhaseTerminating)
	err := a.retireBank(ctx, b)
	if err == nil || !control.Unreachable(err) {
		t.Fatalf("an unreachable live bank was retired: %v", err)
	}
	if !a.bankRetired(b.ID) {
		t.Fatal("the retirement was not saved")
	}
	w, err := a.wishFor(b.ID)
	if err != nil || w.phase != PhaseClosed {
		t.Fatalf("wish after retiring: %+v, %v", w, err)
	}
	setBankPhase(a, b.ID, PhaseClosed)
	if err := a.retireBank(ctx, b); err != nil {
		t.Fatalf("a closed bank that is not running: %v", err)
	}
	setBankPhase(a, b.ID, PhaseTerminating)
	if err := a.ctl.Update(func(st *setup.Status) (bool, error) {
		delete(setup.FindResource(st, setup.KindBank, b.ID).Steps, stepRetired)
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	p, err := participant.New(ctx, a.cfg.Dir, participant.Options{Logger: a.log, BankID: b.ID, CoreURL: coreURL, Bind: "127.0.0.1", DatabaseURL: a.cfg.DatabaseURL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(b.Port))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: p.Server().API(), ReadHeaderTimeout: probeLimit}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ln)
	}()
	t.Cleanup(func() {
		_ = srv.Close()
		<-done
	})
	if err := a.retireBank(ctx, b); err != nil {
		t.Fatal(err)
	}
	if phase, _ := p.Server().Phase(); phase != PhaseClosed {
		t.Fatalf("the bank is %s after it was retired", phase)
	}
}

func verifyRows(t *testing.T, a *App) {
	b := a.cfg.Bank("bank-b")
	a.lifeMu.Lock()
	a.status[b.ID].Steps[stepIndex(config.StepOpenBankingSchema)].Done = false
	a.lifeMu.Unlock()
	rows := []struct {
		name   string
		phase  string
		retire bool
		want   string
	}{
		{"not ready", PhaseActive, false, "Bank B is not ready, so its books cannot be checked. Try again soon."},
		{"closed", PhaseClosed, false, ""},
		{"retired", PhaseTerminating, true, ""},
	}
	for _, r := range rows {
		setBankPhase(a, b.ID, r.phase)
		if r.retire {
			a.recordBank(b.ID, func(res *setup.Resource) bool {
				res.Steps[stepRetired] = state.StepDone
				return true
			})
		}
		a.mu.Lock()
		only := []*config.Bank{b}
		saved := a.cfg.Banks
		a.cfg.Banks = only
		a.mu.Unlock()
		_, err := a.VerifyBank(t.Context())
		a.mu.Lock()
		a.cfg.Banks = saved
		a.mu.Unlock()
		got := ""
		if err != nil {
			got = err.Error()
		}
		var notReady *bankNotReadyError
		if got != r.want || (r.want != "" && !errors.As(err, &notReady)) {
			t.Errorf("%s: got %q, want %q", r.name, got, r.want)
		}
	}
}

func bankContract(t *testing.T, v validator.Validator, core *App, p *participant.Participant, id string, stopNIBSS func()) {
	tok := bankToken(t, core, id)
	call := newContractCall(t, v, p.Server().API())
	norm := func(s string) string { return stampedField.ReplaceAllString(s, `"$1":"T"`) }
	run := func(validate bool) func(name, method, path, key, body string, status int, want string) controlReply {
		return func(name, method, path, key, body string, status int, want string) controlReply {
			t.Helper()
			got := call(method, path, key, body, tok, validate)
			if got.status != status || norm(got.body) != want {
				t.Errorf("%s: got %d %s, want %d %s", name, got.status, norm(got.body), status, want)
			}
			return got
		}
	}
	step, refused := run(true), run(false)
	apiErr := func(code, msg string) string {
		return fmt.Sprintf(`{"error":{"code":%q,"message":%q,"request_id":"req_ID"}}`, code, msg)
	}
	if got := call("PUT", "/v1/phase", "", `{"phase":"active","generation":1,"world_id":"wld_1"}`, tok, true); got.status != 200 {
		t.Fatalf("phase: %d %s", got.status, got.body)
	}
	step("no customers", "GET", "/v1/customers", "", "", 200, `{"data":[]}`)
	step("no accounts", "GET", "/v1/accounts", "", "", 200, `{"data":[]}`)
	adaID := world.ID("cus", "bank-b", "api:cus-ada")
	bolaID := world.ID("cus", "bank-b", "api:cus-bola")
	adaJSON := fmt.Sprintf(`{"id":%q,"name":"Ada Obi","phone":"2348031234567","bvn":"T","status":"active","dob":"1990-04-21","created_at":"T"}`, adaID)
	bolaJSON := fmt.Sprintf(`{"id":%q,"name":"Bola Ade","phone":"2348039876543","bvn":"T","status":"active","dob":"1985-02-03","created_at":"T"}`, bolaID)
	adaBody := `{"name":"Ada Obi","phone":"08031234567","dob":"1990-04-21"}`
	step("create customer", "POST", "/v1/customers", "cus-ada", adaBody, 201, adaJSON)
	if got := call("POST", "/v1/customers", "cus-ada", adaBody, tok, true); !got.replayed || norm(got.body) != adaJSON {
		t.Errorf("customer replay: %+v", got)
	}
	step("customer key reused", "POST", "/v1/customers", "cus-ada", strings.Replace(adaBody, "Ada Obi", "Ada Obi Jr", 1), 422, apiErr("idempotency_key_reused", "This Idempotency-Key was used with a different request."))
	refused("customer without a key", "POST", "/v1/customers", "", adaBody, 400, apiErr("invalid_request", "An Idempotency-Key header of 1 to 255 characters is required on writes."))
	step("customer with a bad phone", "POST", "/v1/customers", "cus-bad", `{"name":"Bo","phone":"123","dob":"1990-04-21"}`, 422, apiErr("invalid_request", `Phone "123" must start with +234 or 0.`))
	refused("customer with a bad date", "POST", "/v1/customers", "cus-date", `{"name":"Bo","phone":"08031234567","dob":"21/04/1990"}`, 422, apiErr("invalid_request", `Dob "21/04/1990" must be a date like 1990-04-21.`))
	refused("customer with an unknown field", "POST", "/v1/customers", "cus-field", `{"name":"Bo","age":3}`, 400, apiErr("invalid_request", `Send {"name":"Ada Obi","phone":"08031234567","dob":"1990-04-21"}, with optional username and password.`))
	step("second customer", "POST", "/v1/customers", "cus-bola", `{"name":"Bola Ade","phone":"+234 803 987 6543","dob":"1985-02-03"}`, 201, bolaJSON)
	step("customers first page", "GET", "/v1/customers?limit=1", "", "", 200, fmt.Sprintf(`{"data":[%s],"next_cursor":%q}`, adaJSON, adaID))
	step("customers last page", "GET", "/v1/customers?limit=1&cursor="+adaID, "", "", 200, fmt.Sprintf(`{"data":[%s]}`, bolaJSON))
	step("customers bad cursor", "GET", "/v1/customers?cursor=cus_nobody", "", "", 400, apiErr("invalid_cursor", "That cursor is not valid. Use next_cursor from the last page."))
	refused("customers bad limit", "GET", "/v1/customers?limit=0", "", "", 422, apiErr("invalid_request", "Limit must be a number from 1 to 1000."))
	step("open account for nobody", "POST", "/v1/accounts", "acc-nobody", `{"customer":"cus_nobody","type":"current"}`, 404, apiErr("not_found", "No such customer."))
	refused("open account of a bad type", "POST", "/v1/accounts", "acc-type", fmt.Sprintf(`{"customer":%q,"type":"fixed"}`, adaID), 422, apiErr("invalid_request", `Account type "fixed" must be current or savings.`))
	opened := call("POST", "/v1/accounts", "acc-ada", fmt.Sprintf(`{"customer":%q,"type":"savings"}`, adaID), tok, true)
	adaAcct := accountNo.FindStringSubmatch(opened.body)[1]
	adaAccount := fmt.Sprintf(`{"nuban":%q,"name":"Ada Obi","customer":%q,"type":"savings","status":"active","currency":"NGN","balance_minor":0}`, adaAcct, adaID)
	if opened.status != 201 || opened.body != adaAccount {
		t.Fatalf("open account: %d %s, want %s", opened.status, opened.body, adaAccount)
	}
	if got := call("POST", "/v1/accounts", "acc-ada", fmt.Sprintf(`{"customer":%q,"type":"savings"}`, adaID), tok, true); !got.replayed || got.body != adaAccount {
		t.Errorf("account replay: %+v", got)
	}
	step("account key reused", "POST", "/v1/accounts", "acc-ada", fmt.Sprintf(`{"customer":%q,"type":"current"}`, adaID), 422, apiErr("idempotency_key_reused", "This Idempotency-Key was used with a different request."))
	cash := func(kind, account string, balance, minor int64) string {
		return fmt.Sprintf(`{"kind":%q,"account":%q,"amount_minor":%d,"balance_minor":%d,"replayed":false,"posted_at":"T"}`, kind, account, minor, balance)
	}
	deposit := fmt.Sprintf(`{"account":%q,"amount":"5,000.00"}`, adaAcct)
	step("deposit", "POST", "/v1/deposits", "cash-1", deposit, 201, cash("deposit", adaAcct, 500000, 500000))
	if got := call("POST", "/v1/deposits", "cash-1", deposit, tok, true); !got.replayed || norm(got.body) != cash("deposit", adaAcct, 500000, 500000) {
		t.Errorf("deposit replay: %+v", got)
	}
	step("deposit key reused", "POST", "/v1/deposits", "cash-1", fmt.Sprintf(`{"account":%q,"amount":"1.00"}`, adaAcct), 422, apiErr("idempotency_key_reused", "This Idempotency-Key was used with a different request."))
	step("deposit into nothing", "POST", "/v1/deposits", "cash-none", `{"account":"0000000000","amount":"1"}`, 404, apiErr("not_found", "No such account."))
	step("deposit of nothing", "POST", "/v1/deposits", "cash-zero", fmt.Sprintf(`{"account":%q,"amount":"0"}`, adaAcct), 422, apiErr("invalid_request", `Amount "0" must be a positive naira amount.`))
	step("withdrawal", "POST", "/v1/withdrawals", "cash-2", fmt.Sprintf(`{"account":%q,"amount":"1200"}`, adaAcct), 201, cash("withdrawal", adaAcct, 380000, 120000))
	step("overdraw", "POST", "/v1/withdrawals", "cash-3", fmt.Sprintf(`{"account":%q,"amount":"100000"}`, adaAcct), 422, apiErr("insufficient_funds", "Account "+adaAcct+" holds ₦3,800.00 and ₦100,000.00 was asked for; withdraw ₦3,800.00 or less."))
	accountsOf := func(customer string, balance int64) string {
		return fmt.Sprintf(`{"data":[{"nuban":%q,"name":"Ada Obi","customer":%q,"type":"savings","status":"active","currency":"NGN","balance_minor":%d}]}`, adaAcct, customer, balance)
	}
	step("balance after the overdraw", "GET", "/v1/accounts?customer="+adaID, "", "", 200, accountsOf(adaID, 380000))
	step("accounts of nobody", "GET", "/v1/accounts?customer="+bolaID, "", "", 200, `{"data":[]}`)
	today := clock.New().Today().String()
	line := func(seq, amount, after int64, narration string) string {
		return fmt.Sprintf(`{"seq":%d,"amount_minor":%d,"balance_after_minor":%d,"posted_at":"T","value_date":%q,"narration":%q}`, seq, amount, after, today, narration)
	}
	deposited, withdrew := line(1, 500000, 500000, "CASH DEPOSIT"), line(2, -120000, 380000, "CASH WITHDRAWAL")
	step("statement", "GET", "/v1/accounts/"+adaAcct+"/statement", "", "", 200, fmt.Sprintf(`{"data":[%s,%s]}`, deposited, withdrew))
	step("statement first page", "GET", "/v1/accounts/"+adaAcct+"/statement?limit=1", "", "", 200, fmt.Sprintf(`{"data":[%s],"next_cursor":"1"}`, deposited))
	step("statement last page", "GET", "/v1/accounts/"+adaAcct+"/statement?limit=1&cursor=1&from="+today+"&to="+today, "", "", 200, fmt.Sprintf(`{"data":[%s]}`, withdrew))
	step("statement of another day", "GET", "/v1/accounts/"+adaAcct+"/statement?from="+clock.New().Today().AddDays(1).String(), "", "", 200, `{"data":[]}`)
	refused("statement with a bad date", "GET", "/v1/accounts/"+adaAcct+"/statement?from=soon", "", "", 422, apiErr("invalid_request", `From "soon" must be a date like 2026-03-01.`))
	step("statement of nothing", "GET", "/v1/accounts/0000000000/statement", "", "", 404, apiErr("not_found", "No such account."))
	step("close an account with money", "POST", "/v1/accounts/"+adaAcct+"/close", "close-1", "", 422, apiErr("balance_not_zero", "Account balance is not zero: account "+adaAcct+" holds ₦3,800.00; move it out by transfer or cash first."))
	step("close a customer with an account", "POST", "/v1/customers/"+adaID+"/close", "close-2", "", 422, apiErr("open_accounts", "Customer has open accounts: customer "+adaID+" still has 1 open account; close them first."))
	step("empty the account", "POST", "/v1/withdrawals", "cash-4", fmt.Sprintf(`{"account":%q,"amount":"3800"}`, adaAcct), 201, cash("withdrawal", adaAcct, 0, 380000))
	step("close the account", "POST", "/v1/accounts/"+adaAcct+"/close", "close-3", "", 200, fmt.Sprintf(`{"nuban":%q,"status":"closed"}`, adaAcct))
	if got := call("POST", "/v1/accounts/"+adaAcct+"/close", "close-3", "", tok, true); !got.replayed {
		t.Errorf("close replay: %+v", got)
	}
	step("deposit into a closed account", "POST", "/v1/deposits", "cash-5", deposit, 422, apiErr("account_restricted", "Account is closed."))
	step("close the customer", "POST", "/v1/customers/"+adaID+"/close", "close-4", "", 200, fmt.Sprintf(`{"customer":%q,"status":"closed"}`, adaID))
	step("close nobody", "POST", "/v1/customers/cus_nobody/close", "close-5", "", 404, apiErr("not_found", "No such customer."))
	step("open an account for a closed customer", "POST", "/v1/accounts", "acc-closed", fmt.Sprintf(`{"customer":%q,"type":"current"}`, adaID), 422, apiErr("customer_closed", "Customer is closed: "+adaID+"."))
	stopNIBSS()
	refused("customer while NIBSS is unreachable", "POST", "/v1/customers", "cus-late", `{"name":"Chi Eze","phone":"08030000002","dob":"1992-02-02"}`, 503, apiErr("unavailable", "Bank B cannot reach NIBSS right now. Try again in a minute."))
	if got := call("GET", "/v1/customers?limit=1000", "", "", tok, false); got.status != 200 || strings.Contains(got.body, "Chi Eze") {
		t.Errorf("customer created without a BVN: %d %s", got.status, got.body)
	}
	if got := call("PUT", "/v1/phase", "", `{"phase":"suspended","reason":"Suspended by the operator.","generation":2,"world_id":"wld_1"}`, tok, true); got.status != 200 {
		t.Fatalf("suspend: %d %s", got.status, got.body)
	}
	step("deposit while suspended", "POST", "/v1/deposits", "cash-6", deposit, 409, apiErr("bank_not_active", "Bank B is suspended, so it cannot do this now."))
	step("read while suspended", "GET", "/v1/accounts?customer="+bolaID, "", "", 200, `{"data":[]}`)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := p.Handler()
	if err != nil {
		t.Fatal(err)
	}
	site := &http.Server{Handler: handler, ReadHeaderTimeout: probeLimit}
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = site.Serve(ln)
	}()
	t.Cleanup(func() {
		_ = site.Close()
		<-served
	})
	host := fmt.Sprintf("bank-b.localhost:%d", p.Server().Bank().Port)
	var wg sync.WaitGroup
	wg.Add(1)
	failures := make(chan error, reloads)
	go func() {
		defer wg.Done()
		defer close(failures)
		for range reloads {
			req, err := http.NewRequest(http.MethodGet, "http://"+ln.Addr().String()+"/login", nil)
			if err != nil {
				failures <- err
				return
			}
			req.Host = host
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				failures <- err
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()
	for i := range reloads {
		renamed := *core.cfg.Bank("bank-b")
		renamed.Name = fmt.Sprintf("Bank B %d", i)
		if err := core.cfg.EnsureBankDir(&renamed); err != nil {
			t.Fatal(err)
		}
		if got := call("PUT", "/v1/phase", "", `{"phase":"active","generation":2,"world_id":"wld_1"}`, tok, false); got.status != 200 {
			t.Fatalf("reload: %d %s", got.status, got.body)
		}
	}
	wg.Wait()
	for err := range failures {
		t.Error(err)
	}
}

func TestHolderOfNeedsTheCustomer(t *testing.T) {
	holders := map[string]Customer{"cus_1": {ID: "cus_1", Name: "Ada Obi"}}
	rows := []struct {
		name, owner, want string
	}{
		{"known", "cus_1", "Ada Obi"},
		{"unknown", "cus_2", "read customer cus_2: bank: unknown customer"},
	}
	for _, r := range rows {
		c, err := holderOf(holders, r.owner)
		got := c.Name
		if err != nil {
			got = err.Error()
		}
		if got != r.want {
			t.Errorf("%s: got %q, want %q", r.name, got, r.want)
		}
	}
}
