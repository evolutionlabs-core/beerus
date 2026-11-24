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
	"slices"
	"strconv"
	"sync"
	"testing"

	validator "github.com/pb33f/libopenapi-validator"

	"github.com/evolutionlabs-core/beerus/internal/bank/participant"
	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/state"
	"github.com/evolutionlabs-core/beerus/internal/world"
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

func startCore(t *testing.T, core *App) string {
	t.Helper()
	ln := loopback(t, "127.0.0.1:0")
	core.cfg.Connect.ControlPort = ln.Addr().(*net.TCPAddr).Port
	return serve(t, ln, core.ControlHandler())
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
	base := serve(t, loopback(t, "127.0.0.1:0"), p.Server().API())
	c := &bankAdmin{link: control.NewLink(bankToken(t, core, id), ""), id: id, name: core.cfg.Bank(id).Name, base: base}
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
	type built struct {
		v   validator.Validator
		err error
	}
	spec := make(chan built, 1)
	go func() {
		v, err := participantValidator()
		spec <- built{v, err}
	}()
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
	b := <-spec
	if b.err != nil {
		t.Fatalf("validator: %v", b.err)
	}
	t.Run("participant control contract", func(t *testing.T) { controlContract(t, b.v) })
	t.Run("participant bank contract", func(t *testing.T) {
		bankContract(t, b.v, core, bankOn(t, core, coreURL, "bank-b", true), stopNIBSS)
	})
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

func TestHolderOfNeedsTheCustomer(t *testing.T) {
	holders := map[string]Customer{"cus_1": {ID: "cus_1", Name: "Ada Obi"}}
	if c, err := holderOf(holders, "cus_1"); err != nil || c.Name != "Ada Obi" {
		t.Errorf("known: %v, %v", c, err)
	}
	if _, err := holderOf(holders, "cus_2"); err == nil || err.Error() != "read customer cus_2: bank: unknown customer" {
		t.Errorf("unknown: %v", err)
	}
}

func bankAdminRows(t *testing.T, c *bankAdmin) {
	ctx := t.Context()
	var made Customer
	if err := c.call(ctx, http.MethodPost, bankCustomers, "cus-ada", control.CustomerRequest{Name: "Ada Obi", Phone: "08031234567", DOB: "1990-04-21"}, &made); err != nil {
		t.Fatal(err)
	}
	opened, err := c.openAccount(ctx, "acc-ada", made.ID, "savings")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.call(ctx, http.MethodPost, "/v1/deposits", "cash-1", server.CashRequest{Account: opened.NUBAN, Amount: "5,000.00"}, nil); err != nil {
		t.Fatal(err)
	}
	lines, _, err := c.recent(ctx, opened.NUBAN, 50, "")
	if err != nil || len(lines) != 1 {
		t.Fatalf("recent: %v, %v", lines, err)
	}
	same := "same key"
	notClosing := "Bank A is active, so it cannot pay out or freeze accounts; that happens only while a bank is closing."
	runChecks(t, []check{
		{"customers", func() (string, error) { v, err := c.customers(ctx); return fmt.Sprint(len(v), " ", v[0].Name), err }, "1 Ada Obi"},
		{"customers by unknown BVN", func() (string, error) { v, err := c.customersByBVN(ctx, "22900000000"); return fmt.Sprint(len(v)), err }, "0"},
		{"create customer with a used key and another body", func() (string, error) {
			_, err := c.createCustomer(ctx, "cus-ada", Customer{Name: "Chi Eze", Phone: "08030000002", DOB: made.DOB})
			return same, err
		}, "This Idempotency-Key was used with a different request."},
		{"account", func() (string, error) {
			v, err := c.account(ctx, opened.NUBAN)
			return fmt.Sprint(v.Name, " ", v.Balance), err
		}, "Ada Obi 500000"},
		{"unknown account", func() (string, error) { _, err := c.account(ctx, "0000000000"); return "", err }, "ledger: unknown account: 0000000000"},
		{"accounts with a bad cursor", func() (string, error) { _, _, err := c.accounts(ctx, "", 10, "nope"); return "", err }, `bank: invalid cursor: "nope"`},
		{"totals", func() (string, error) { v, err := c.totals(ctx); return fmt.Sprint(v), err }, "{1 1 500000}"},
		{"ledger check", func() (string, error) { v, err := c.check(ctx); return v.String(), err }, "trial balance 0, balance mismatches [], event mismatches [], unbalanced entries []"},
		{"unknown entry", func() (string, error) { _, err := c.line(ctx, opened.NUBAN, "ln_none"); return "", err }, "bank: unknown statement line: ln_none"},
		{"payout while active", func() (string, error) { return "", c.payout(ctx, "p:"+opened.NUBAN, opened.NUBAN, 100) }, notClosing},
		{"freeze while active", func() (string, error) { return "", c.freeze(ctx, "f:"+opened.NUBAN, opened.NUBAN) }, notClosing},
	})
	closing := control.PhaseRequest{Phase: PhaseClosing, Generation: 2, WorldID: "wld_1"}
	if err := c.call(ctx, http.MethodPut, "/v1/phase", "", closing, nil); err != nil {
		t.Fatal(err)
	}
	key := "closing:" + opened.NUBAN
	runChecks(t, []check{
		{"payout", func() (string, error) { return "", c.payout(ctx, key, opened.NUBAN, 120000) }, ""},
		{"payout again with the same key", func() (string, error) { return "", c.payout(ctx, key, opened.NUBAN, 120000) }, ""},
		{"payout with the same key and another amount", func() (string, error) { return "", c.payout(ctx, key, opened.NUBAN, 5) }, "This Idempotency-Key was used with a different request."},
		{"payouts", func() (string, error) { v, err := c.payouts(ctx); return fmt.Sprint(v[opened.NUBAN]), err }, "120000"},
		{"balance", func() (string, error) { v, err := c.account(ctx, opened.NUBAN); return fmt.Sprint(v.Balance), err }, "380000"},
		{"freeze", func() (string, error) { return "", c.freeze(ctx, key+":freeze", opened.NUBAN) }, ""},
		{"status after the freeze", func() (string, error) { v, err := c.account(ctx, opened.NUBAN); return v.Status, err }, "frozen"},
	})
}

func retireRows(t *testing.T, a *App, coreURL string) {
	b := a.cfg.Bank("bank-a")
	if a.bankRetired(b.ID) {
		t.Fatal("a bank counted as retired before it was")
	}
	setBankPhase(a, b.ID, PhaseTerminating)
	if err := a.retireBank(t.Context(), b); err == nil || !control.Unreachable(err) {
		t.Fatalf("an unreachable live bank was retired: %v", err)
	}
	if w, err := a.wishFor(b.ID); !a.bankRetired(b.ID) || err != nil || w.phase != PhaseClosed {
		t.Fatalf("the retirement was not saved: %+v, %v", w, err)
	}
	setBankPhase(a, b.ID, PhaseClosed)
	if err := a.retireBank(t.Context(), b); err != nil {
		t.Fatalf("a closed bank that is not running: %v", err)
	}
	setBankPhase(a, b.ID, PhaseTerminating)
	if err := a.ctl.Update(func(st *setup.Status) (bool, error) {
		delete(setup.FindResource(st, setup.KindBank, b.ID).Steps, stepRetired)
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	p := bankOn(t, a, coreURL, b.ID, false)
	serve(t, loopback(t, "127.0.0.1:"+strconv.Itoa(b.Port)), p.Server().API())
	if err := a.retireBank(t.Context(), b); err != nil {
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
	for _, r := range []struct {
		name, phase, want string
		retire            bool
	}{
		{"not ready", PhaseActive, "Bank B is not ready, so its books cannot be checked. Try again soon.", false},
		{"closed", PhaseClosed, "", false},
		{"retired", PhaseTerminating, "", true},
	} {
		setBankPhase(a, b.ID, r.phase)
		if r.retire {
			a.recordBank(b.ID, func(res *setup.Resource) bool {
				res.Steps[stepRetired] = state.StepDone
				return true
			})
		}
		a.mu.Lock()
		saved := a.cfg.Banks
		a.cfg.Banks = []*config.Bank{b}
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

func bankContract(t *testing.T, v validator.Validator, core *App, p *participant.Participant, stopNIBSS func()) {
	const id = "bank-b"
	tok := bankToken(t, core, id)
	base := serve(t, loopback(t, "127.0.0.1:0"), p.Server().API())
	vars := map[string]string{"{ada}": world.ID("cus", id, "api:cus-ada"), "{bola}": world.ID("cus", id, "api:cus-bola")}
	const (
		reused  = "This Idempotency-Key was used with a different request."
		ada     = `{"name":"Ada Obi","phone":"08031234567","dob":"1990-04-21"}`
		deposit = `{"account":"{acc}","amount":"5,000.00"}`
	)
	run := func(rows ...endpoint) { runEndpoints(t, v, base, tok, vars, rows) }
	run(
		endpoint{"no customers", "GET /v1/customers", "", 200, `{"data":[]}`},
		endpoint{"create customer", "POST /v1/customers key=cus-ada", ada, 201, `"id":"{ada}","name":"Ada Obi","phone":"2348031234567"`},
		endpoint{"customer replay", "POST /v1/customers key=cus-ada replay", ada, 201, `"id":"{ada}"`},
		endpoint{"customer key reused", "POST /v1/customers key=cus-ada", `{"name":"Ada Obi Jr","phone":"08031234567","dob":"1990-04-21"}`, 422, reused},
		endpoint{"customer with a bad phone", "POST /v1/customers key=cus-bad", `{"name":"Bo","phone":"123","dob":"1990-04-21"}`, 422, `Phone "123" must start with +234 or 0.`},
		endpoint{"second customer", "POST /v1/customers key=cus-bola", `{"name":"Bola Ade","phone":"+234 803 987 6543","dob":"1985-02-03"}`, 201, `"id":"{bola}","name":"Bola Ade","phone":"2348039876543"`},
		endpoint{"customers first page", "GET /v1/customers?limit=1", "", 200, `"next_cursor":"{ada}"`},
		endpoint{"customers last page", "GET /v1/customers?limit=1&cursor={ada}", "", 200, `"id":"{bola}"`},
		endpoint{"customers bad cursor", "GET /v1/customers?cursor=cus_nobody", "", 400, "That cursor is not valid. Use next_cursor from the last page."},
		endpoint{"open account", "POST /v1/accounts key=acc-ada", `{"customer":"{ada}","type":"savings"}`, 201, `"type":"savings","status":"active","currency":"NGN","balance_minor":0`},
		endpoint{"deposit", "POST /v1/deposits key=cash-1", deposit, 201, `"amount_minor":500000,"balance_minor":500000`},
		endpoint{"deposit into nothing", "POST /v1/deposits key=cash-none", `{"account":"0000000000","amount":"1"}`, 404, "No such account."},
		endpoint{"deposit of nothing", "POST /v1/deposits key=cash-zero", `{"account":"{acc}","amount":"0"}`, 422, `Amount "0" must be a positive naira amount.`},
		endpoint{"withdrawal", "POST /v1/withdrawals key=cash-2", `{"account":"{acc}","amount":"1200"}`, 201, `"amount_minor":120000,"balance_minor":380000`},
		endpoint{"overdraw", "POST /v1/withdrawals key=cash-3", `{"account":"{acc}","amount":"100000"}`, 422, "holds ₦3,800.00 and ₦100,000.00 was asked for; withdraw ₦3,800.00 or less."},
		endpoint{"balance after the overdraw", "GET /v1/accounts?customer={ada}", "", 200, `"balance_minor":380000`},
		endpoint{"accounts of nobody", "GET /v1/accounts?customer={bola}", "", 200, `{"data":[]}`},
		endpoint{"statement", "GET /v1/accounts/{acc}/statement", "", 200, `"narration":"CASH DEPOSIT"`},
		endpoint{"statement first page", "GET /v1/accounts/{acc}/statement?limit=1", "", 200, `"next_cursor":"1"`},
		endpoint{"close an account with money", "POST /v1/accounts/{acc}/close key=close-1", "", 422, "holds ₦3,800.00; move it out by transfer or cash first."},
		endpoint{"close a customer with an account", "POST /v1/customers/{ada}/close key=close-2", "", 422, "still has 1 open account; close them first."},
		endpoint{"empty the account", "POST /v1/withdrawals key=cash-4", `{"account":"{acc}","amount":"3800"}`, 201, `"balance_minor":0`},
		endpoint{"close the account", "POST /v1/accounts/{acc}/close key=close-3", "", 200, `"status":"closed"`},
		endpoint{"deposit into a closed account", "POST /v1/deposits key=cash-5", deposit, 422, "Account is closed."},
		endpoint{"close the customer", "POST /v1/customers/{ada}/close key=close-4", "", 200, `"status":"closed"`},
	)
	stopNIBSS()
	run(endpoint{"customer while NIBSS is unreachable", "POST /v1/customers key=cus-late raw", `{"name":"Chi Eze","phone":"08030000002","dob":"1992-02-02"}`, 503, "Bank B cannot reach NIBSS right now. Try again in a minute."})
	c := &bankAdmin{link: control.NewLink(tok, ""), id: id, name: "Bank B", base: base}
	if all, err := c.customers(t.Context()); err != nil || slices.ContainsFunc(all, func(x Customer) bool { return x.Name == "Chi Eze" }) {
		t.Errorf("a customer was created without a BVN: %v, %v", all, err)
	}
	phase := func(generation int, state string) endpoint {
		return endpoint{"phase " + state, "PUT /v1/phase raw", fmt.Sprintf(`{"phase":%q,"reason":"Suspended by the operator.","generation":%d,"world_id":"wld_1"}`, state, generation), 200, `"phase":"` + state + `"`}
	}
	run(
		phase(2, "suspended"),
		endpoint{"deposit while suspended", "POST /v1/deposits key=cash-6", deposit, 409, "Bank B is suspended, so it cannot do this now."},
		endpoint{"read while suspended", "GET /v1/accounts?customer={bola}", "", 200, `{"data":[]}`},
	)
	site, err := p.Handler()
	if err != nil {
		t.Fatal(err)
	}
	siteURL := serve(t, loopback(t, "127.0.0.1:0"), site)
	host := fmt.Sprintf("bank-b.localhost:%d", p.Server().Bank().Port)
	var wg sync.WaitGroup
	failures := make(chan error, reloads)
	wg.Go(func() {
		for range reloads {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, siteURL+"/login", nil)
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
	})
	for i := range reloads {
		renamed := *core.cfg.Bank(id)
		renamed.Name = fmt.Sprintf("Bank B %d", i)
		if err := core.cfg.EnsureBankDir(&renamed); err != nil {
			t.Fatal(err)
		}
		run(phase(3, "active"))
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
}
