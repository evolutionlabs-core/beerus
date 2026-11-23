package contracttest

import (
	"cmp"
	"context"
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"github.com/evolutionlabs-core/beerus/internal/postgres"
	"io"
	"io/fs"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/banktest"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

const (
	soloDeposit  = 50_000_000
	soloCredit   = 2_500_000
	soloRefused  = 1_000_000
	soloStop     = 30 * time.Second
	soloPassword = "correct horse battery"
)

func dirListing(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == root {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

type soloBank struct {
	ctx     context.Context
	dbURL   string
	stage   string
	own     *config.Bank
	books   *bank.Bank
	account ledger.Account
	solo    string
	site    string
	api     string
	probe   *bankProbe
	nibss   *http.Client
}

func seedSoloBank(t *testing.T, ctx context.Context, dbURL, stage string, own *config.Bank, now time.Time) (*bank.Bank, ledger.Account) {
	t.Helper()
	st, err := store.Open(ctx, dbURL, own.Database, openbanking.Migrate)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Drop(ctx) })
	fees := banktest.Fees(t)
	books := bank.New(ledger.New(st.Pool, nil), bank.Profile{ID: own.ID, Name: own.Name, NIPCode: own.NIPCode, NUBANCode: own.NUBANCode}, fees)
	if err := books.Setup(ctx); err != nil {
		t.Fatal(err)
	}
	if err := books.Genesis(ctx, control.GenesisSettlement, control.GenesisVault, now); err != nil {
		t.Fatal(err)
	}
	hash, err := bank.HashPassword(soloPassword)
	if err != nil {
		t.Fatal(err)
	}
	customer, err := books.CreateCustomer(ctx, "cus-ada", "ADA OBI", "+2348030000001", clock.Date{Y: 1990, M: 1, D: 1}, "22912345678", "ada", hash, now)
	if err != nil {
		t.Fatal(err)
	}
	account, err := books.OpenAccount(ctx, "acc-ada", customer.ID, bank.ProductCurrent, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := books.CashDeposit(ctx, "dep-ada", account.NUBAN, soloDeposit, now); err != nil {
		t.Fatal(err)
	}
	return books, account
}

func runSolo(t *testing.T, id, dir, dbURL string) (*lockedBuffer, <-chan error, func()) {
	t.Helper()
	c := newCLIAt(t, dir)
	run := exec.Command(c.bin, "run", "--participant", id, "--dir", dir)
	run.Env = c.env(config.DatabaseURLEnv + "=" + dbURL)
	logs := &lockedBuffer{}
	run.Stdout, run.Stderr = logs, logs
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- run.Wait() }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = run.Process.Signal(syscall.SIGTERM)
			select {
			case <-exited:
			case <-time.After(soloStop):
				_ = run.Process.Kill()
				<-exited
			}
		})
	}
	t.Cleanup(stop)
	return logs, exited, stop
}

func startSoloBank(t *testing.T, state string) soloBank {
	t.Helper()
	if testing.Short() {
		t.Skip("starts a bank process; run without -short")
	}
	ctx := context.WithoutCancel(t.Context())
	dbURL := cmp.Or(os.Getenv(config.DatabaseURLEnv), os.Getenv("BEERUS_TEST_DATABASE_URL"), "postgres:///postgres")
	now := clock.New().Now()
	stage := filepath.Join(t.TempDir(), "stage")
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	ports := freePorts(t, 5)
	cfg := &config.Config{Dir: stage, SetupKey: soloSetupKey, Connect: config.Connect{Host: "connect.localhost", BrandName: "Connect", WidgetPort: ports[2], ControlPort: ports[3], NIBSSPort: ports[4]}}
	if err := config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}
	own := &config.Bank{State: state, ID: "bank-a", Name: "Bank A", Type: "commercial", NIPCode: "000001", NUBANCode: "001", HistoryDays: 730, Port: ports[0], APIPort: ports[1], Database: config.DatabaseName(stage, "bank-a")}
	if err := cfg.EnsureBankDir(own); err != nil {
		t.Fatal(err)
	}
	other := config.DatabaseName(stage, "bank-b")
	if err := store.Create(ctx, dbURL, other); err != nil {
		t.Skipf("no Postgres for the solo bank test (%v); set BEERUS_TEST_DATABASE_URL", err)
	}
	t.Cleanup(func() { _ = postgres.DropDatabases(ctx, dbURL, []string{other}) })
	books, account := seedSoloBank(t, ctx, dbURL, stage, own, now)

	entries, err := os.ReadDir(stage)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "banks" {
			if err := os.RemoveAll(filepath.Join(stage, e.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
	solo := stage
	logs, exited, _ := runSolo(t, own.ID, solo, dbURL)
	site := fmt.Sprintf("http://127.0.0.1:%d", own.Port)
	api := fmt.Sprintf("http://127.0.0.1:%d", own.APIPort)
	probe := &bankProbe{t: t, plain: &http.Client{Timeout: controlProbeTimeout}}
	waitFor(t, waitLimit, "the bank to answer on its own", func() bool {
		select {
		case err := <-exited:
			t.Fatalf("the bank exited: %v\n%s", err, logs.String())
		default:
		}
		code, _ := probe.get(probe.plain, site+"/health", nil)
		return code == http.StatusOK
	})
	nibss := callerClient(t, config.NIBSSCN)
	return soloBank{ctx, dbURL, stage, own, books, account, solo, site, api, probe, nibss}
}

func TestSoloBankServesWithNothingElseRunning(t *testing.T) {
	s := startSoloBank(t, "")
	ctx, dbURL, stage, own, books, account, solo, site, api, probe, nibss := s.ctx, s.dbURL, s.stage, s.own, s.books, s.account, s.solo, s.site, s.api, s.probe, s.nibss
	if code, _ := probe.get(probe.plain, site+"/login", nil); code != http.StatusOK {
		t.Fatalf("the login page answered %d, want 200", code)
	}

	session := func(n int) string { return banktest.Session(own.NIPCode, n) }
	reply := func(sid, code string) string {
		return fmt.Sprintf(`{"session_id":%q,"response_code":%q}`, sid, code)
	}
	rows := []struct {
		name         string
		method, path string
		body         string
		want         string
		replayed     bool
		balance      int64
	}{
		{"name enquiry", "GET", "/v1/nip/account-names/" + account.NUBAN, "", fmt.Sprintf(`{"response_code":"00","account_number":%q,"account_name":"ADA OBI"}`, account.NUBAN), false, soloDeposit},
		{"credit", "POST", "/v1/nip/credits", banktest.CreditBody(session(1), account.NUBAN, soloCredit, "Rent"), reply(session(1), "00"), false, soloDeposit + soloCredit},
		{"credit replay", "POST", "/v1/nip/credits", banktest.CreditBody(session(1), account.NUBAN, soloCredit, "Rent"), reply(session(1), "00"), true, soloDeposit + soloCredit},
		{"status credited", "GET", "/v1/nip/credits/" + session(1), "", reply(session(1), "00"), false, soloDeposit + soloCredit},
		{"status unknown", "GET", "/v1/nip/credits/" + session(2), "", reply(session(2), "25"), false, soloDeposit + soloCredit},
		{"credit after 25", "POST", "/v1/nip/credits", banktest.CreditBody(session(2), account.NUBAN, soloRefused, ""), reply(session(2), "25"), false, soloDeposit + soloCredit},
		{"status after refused credit", "GET", "/v1/nip/credits/" + session(2), "", reply(session(2), "25"), false, soloDeposit + soloCredit},
	}
	for _, r := range rows {
		req, err := http.NewRequest(r.method, api+r.path, strings.NewReader(r.body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := nibss.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", r.name, err)
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("%s: %v", r.name, err)
		}
		replayed := resp.Header.Get("Idempotent-Replayed") == "true"
		if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(raw)) != r.want || replayed != r.replayed {
			t.Fatalf("%s: got %d %s replayed=%v, want 200 %s replayed=%v", r.name, resp.StatusCode, strings.TrimSpace(string(raw)), replayed, r.want, r.replayed)
		}
		if got, err := books.L.Balance(ctx, bank.CustomerAccount(account.NUBAN)); err != nil || got != r.balance {
			t.Fatalf("%s: balance %d (%v), want %d", r.name, got, err, r.balance)
		}
	}

	token, err := config.ReadSecret(filepath.Join(config.BankDir(solo, own.ID), config.BankTokenFile))
	if err != nil {
		t.Fatal(err)
	}
	enrol, err := http.NewRequest(http.MethodPost, site+"/v1/customers", strings.NewReader(`{"name":"Bola Ade","phone":"08030000001","dob":"1991-01-01"}`))
	if err != nil {
		t.Fatal(err)
	}
	enrol.Header.Set("Authorization", "Bearer "+token)
	enrol.Header.Set("Idempotency-Key", "cus-bola")
	resp, err := probe.plain.Do(enrol)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("a customer needs a BVN from core, which is down; got %d, want 503", resp.StatusCode)
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	browser := &http.Client{Timeout: controlProbeTimeout, Jar: jar}
	page := func(method, path string, form url.Values) (int, string) {
		req, err := http.NewRequest(method, site+path, strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := browser.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, string(raw)
	}
	_, verify := page("POST", "/login", url.Values{"username": {"ada"}, "password": {soloPassword}})
	code := regexp.MustCompile(`verification code is <span[^>]*>(\d{6})</span>`).FindStringSubmatch(verify)
	if code == nil {
		t.Fatalf("no code on the verify page: %.300s", verify)
	}
	page("POST", "/verify", url.Values{"otp": {code[1]}})
	status, body := page("GET", "/transfer", nil)
	if want := "Bank A cannot reach NIBSS right now. Try again in a minute."; status != http.StatusServiceUnavailable || strings.TrimSpace(body) != want {
		t.Fatalf("the transfer page answered %d %q, want 503 %q", status, strings.TrimSpace(body), want)
	}

	files := []string{"banks", "banks/bank-a"}
	for _, f := range []string{"bank.json", "control-token"} {
		files = append(files, "banks/bank-a/"+f)
	}
	if got := dirListing(t, solo); !slices.Equal(got, files) {
		t.Fatalf("the bank directory holds %v, want %v", got, files)
	}
	for _, f := range []string{"bank.json", "control-token"} {
		fi, err := os.Stat(filepath.Join(config.BankDir(solo, own.ID), f))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s: mode %v, error %v, want 0600", f, fi, err)
		}
	}
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	prefix := strings.TrimSuffix(config.DatabaseName(stage, "x"), "x")
	rowsDB, err := conn.Query(ctx, `SELECT DISTINCT datname FROM pg_stat_activity WHERE datname LIKE $1 ORDER BY datname`, strings.ReplaceAll(prefix, "_", `\_`)+"%")
	if err != nil {
		t.Fatal(err)
	}
	opened, err := pgx.CollectRows(rowsDB, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(opened, []string{own.Database}) {
		t.Fatalf("databases open: %v, want only %s", opened, own.Database)
	}
}

func TestClosedSoloBankRefusesEverything(t *testing.T) {
	s := startSoloBank(t, "closed")
	rows := []struct {
		name   string
		client *http.Client
		url    string
		body   string
		want   string
	}{
		{"nip credit", s.nibss, s.api + "/v1/nip/credits", banktest.CreditBody(banktest.Session(s.own.NIPCode, 1), s.account.NUBAN, soloCredit, "Rent"), `{"error":{"code":"bank_closed","message":"Bank A is closed.","request_id":"`},
		{"site", s.probe.plain, s.site + "/login", "", "Bank A is closed.\n"},
	}
	for _, r := range rows {
		method := http.MethodGet
		if r.body != "" {
			method = http.MethodPost
		}
		req, err := http.NewRequest(method, r.url, strings.NewReader(r.body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := r.client.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", r.name, err)
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("%s: %v", r.name, err)
		}
		if resp.StatusCode != http.StatusConflict || !strings.HasPrefix(string(raw), r.want) {
			t.Fatalf("%s: got %d %s, want 409 %s", r.name, resp.StatusCode, raw, r.want)
		}
	}
	if got, err := s.books.L.Balance(s.ctx, bank.CustomerAccount(s.account.NUBAN)); err != nil || got != soloDeposit {
		t.Fatalf("balance %d (%v), want %d: a closed bank credited something", got, err, soloDeposit)
	}
}
