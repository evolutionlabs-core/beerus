package connect

import (
	"errors"
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/participant"
	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

func bankAdminOnServer(t *testing.T, ensure bool) *bankAdmin {
	t.Helper()
	opts := Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	core, _ := newTestAppWithNIBSS(t, opts)
	coreURL := startCore(t, core)
	p, err := participant.New(t.Context(), core.cfg.Dir, participant.Options{Logger: opts.Logger, BankID: "bank-a", CoreURL: coreURL, Bind: "127.0.0.1", DatabaseURL: core.cfg.DatabaseURL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	if ensure {
		p.Ensure(t.Context())
	}
	tok, err := config.ReadSecret(config.BankDir(core.cfg.Dir, "bank-a") + "/" + config.BankTokenFile)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
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
	c := &bankAdmin{link: control.NewLink(tok, ""), id: "bank-a", name: "Bank A", base: "http://" + ln.Addr().String()}
	phase := control.PhaseRequest{Phase: PhaseActive, Generation: 1, WorldID: "wld_1"}
	if err := c.call(t.Context(), http.MethodPut, "/v1/phase", "", phase, nil); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBankAdminAgainstABankServer(t *testing.T) {
	c := bankAdminOnServer(t, true)
	ctx := t.Context()
	body := control.CustomerRequest{Name: "Ada Obi", Phone: "08031234567", DOB: "1990-04-21", Username: "ada", Password: "correct horse battery"}
	var made bank.Customer
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
	bola := bank.Customer{Name: "Bola Ade", Phone: "08039876543", DOB: made.DOB}
	var bolaID string
	rows := []struct {
		name string
		got  func() (string, error)
		want string
	}{
		{"state", func() (string, error) {
			s, err := c.state(ctx)
			ids := ""
			for _, st := range s.Steps {
				if st.Done {
					ids += " " + st.ID
				}
			}
			return s.ParticipantID + " " + s.Phase + ids, err
		}, "bank-a active allocate directory database ledger_schema openbanking_schema certs cbn site api"},
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
			_, err := c.createCustomer(ctx, "cus-bola", bank.Customer{Name: "Chi Eze", Phone: "08030000002", DOB: made.DOB})
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
		{"login", func() (string, error) {
			id, err := c.login(ctx, "ada", "correct horse battery")
			return fmt.Sprint(id == made.ID), err
		}, "true"},
		{"bad login", func() (string, error) { _, err := c.login(ctx, "ada", "wrong"); return "", err }, "bank: wrong username or password"},
		{"payout while active", func() (string, error) { return "", c.payout(ctx, "close:bank-a:"+opened.NUBAN, opened.NUBAN, 100) }, "Bank A is active, so it cannot pay out or freeze accounts; that happens only while a bank is closing."},
		{"freeze while active", func() (string, error) { return "", c.freeze(ctx, "close:bank-a:"+opened.NUBAN+":freeze", opened.NUBAN) }, "Bank A is active, so it cannot pay out or freeze accounts; that happens only while a bank is closing."},
	}
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

func TestBankAdminClosurePayoutsReplay(t *testing.T) {
	c := bankAdminOnServer(t, true)
	ctx := t.Context()
	var made bank.Customer
	body := control.CustomerRequest{Name: "Ada Obi", Phone: "08031234567", DOB: "1990-04-21"}
	if err := c.call(ctx, http.MethodPost, bankCustomers, "cus-ada", body, &made); err != nil {
		t.Fatal(err)
	}
	opened, err := c.openAccount(ctx, "acc-ada", made.ID, "current")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.call(ctx, http.MethodPost, "/v1/deposits", "cash-1", server.CashRequest{Account: opened.NUBAN, Amount: "5000"}, nil); err != nil {
		t.Fatal(err)
	}
	closing := control.PhaseRequest{Phase: PhaseClosing, Generation: 2, WorldID: "wld_1"}
	if err := c.call(ctx, http.MethodPut, "/v1/phase", "", closing, nil); err != nil {
		t.Fatal(err)
	}
	key := "close:bank-a:" + opened.NUBAN
	rows := []struct {
		name string
		run  func() (string, error)
		want string
	}{
		{"payout", func() (string, error) { return "", c.payout(ctx, key, opened.NUBAN, 120000) }, ""},
		{"payout again with the same key", func() (string, error) { return "", c.payout(ctx, key, opened.NUBAN, 120000) }, ""},
		{"payouts", func() (string, error) { v, err := c.payouts(ctx); return fmt.Sprint(v[opened.NUBAN]), err }, "120000"},
		{"balance", func() (string, error) { v, err := c.account(ctx, opened.NUBAN); return fmt.Sprint(v.Balance), err }, "380000"},
		{"payout with the same key and another amount", func() (string, error) { return "", c.payout(ctx, key, opened.NUBAN, 5) }, "This Idempotency-Key was used with a different request."},
		{"freeze", func() (string, error) { return "", c.freeze(ctx, key+":freeze", opened.NUBAN) }, ""},
		{"status after the freeze", func() (string, error) { v, err := c.account(ctx, opened.NUBAN); return v.Status, err }, "frozen"},
	}
	for _, r := range rows {
		got, err := r.run()
		if err != nil {
			got = err.Error()
		}
		if got != r.want {
			t.Errorf("%s: got %q, want %q", r.name, got, r.want)
		}
	}
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

func TestBankAdminTreatsABankWithoutBooksAsAway(t *testing.T) {
	c := bankAdminOnServer(t, false)
	_, err := c.totals(t.Context())
	var down *bankDownError
	if !errors.As(err, &down) || down.Name != "Bank A" {
		t.Fatalf("got %v, want a down error for Bank A", err)
	}
	if want := "bank-a is not answering: The bank database is not ready yet. Try again soon."; err.Error() != want {
		t.Fatalf("got %q, want %q", err.Error(), want)
	}
}

func TestHolderOfNeedsTheCustomer(t *testing.T) {
	holders := map[string]bank.Customer{"cus_1": {ID: "cus_1", Name: "Ada Obi"}}
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
