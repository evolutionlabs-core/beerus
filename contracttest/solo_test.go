package contracttest

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/banktest"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/postgres"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

const (
	soloSetupKey = 1<<63 + 5
	soloWorld    = "wld_solo"
	soloPassword = "correct horse battery"
	soloDeposit  = 50_000_000
	soloAmount   = 2_500_000
	soloCap      = 1_000_000_000_000
	soloBelowCap = 3_000_000
	soloStop     = 30 * time.Second
)

var (
	requestIDText = regexp.MustCompile(`"request_id":"req_[A-Za-z0-9]+"`)
	stampText     = regexp.MustCompile(`"(registered_at|changed_at|posted_at)":"[^"]*"`)
	sessionText   = regexp.MustCompile(`^[0-9]{30}$`)
)

type soloBank struct {
	cfg   *config.Bank
	books *bank.Bank
	nuban string
	owner string
	site  string
	api   string
	token string
	stop  func()
	logs  *lockedBuffer
}

type solo struct {
	t       *testing.T
	ctx     context.Context
	dir     string
	dbURL   string
	banks   map[string]*soloBank
	clients map[string]*http.Client
	plain   *http.Client
	control string
	dirURL  string
	token   string
}

func soloErr(code, message string) string {
	return fmt.Sprintf(`{"error":{"code":%q,"message":%q,"request_id":"req_ID"}}`, code, message)
}

func newSolo(t *testing.T, withNIBSS bool, tsqAfter time.Duration, state string, ids ...string) *solo {
	t.Helper()
	ctx := context.WithoutCancel(t.Context())
	dbURL := cmp.Or(os.Getenv(config.DatabaseURLEnv), os.Getenv("BEERUS_TEST_DATABASE_URL"), "postgres:///postgres")
	dir := filepath.Join(t.TempDir(), "stage")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ports := freePorts(t, 5+2*len(ids))
	cfg := &config.Config{Dir: dir, SetupKey: soloSetupKey, TSQAfter: tsqAfter, Connect: config.Connect{Host: "connect.localhost", BrandName: "Connect", WidgetPort: ports[0], ControlPort: ports[1], NIBSSPort: ports[2], NIBSSControlPort: ports[3], CBNPort: ports[4]}}
	if err := config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}
	s := &solo{t: t, ctx: ctx, dir: dir, dbURL: dbURL, banks: map[string]*soloBank{}, clients: map[string]*http.Client{}, plain: &http.Client{Timeout: 30 * time.Second}}
	if withNIBSS {
		if err := cfg.EnsureNIBSSDir(soloWorld); err != nil {
			t.Fatal(err)
		}
		name := config.DatabaseName(dir, config.NIBSSID)
		if err := store.Create(ctx, dbURL, name); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = postgres.DropDatabases(ctx, dbURL, []string{name}) })
		s.control, s.dirURL = fmt.Sprintf("http://127.0.0.1:%d", ports[3]), fmt.Sprintf("http://127.0.0.1:%d", ports[2])
	}
	for i, id := range append(ids, "bank-z") {
		s.clients[id] = callerClient(t, id)
		if i == len(ids) {
			break
		}
		code := fmt.Sprintf("0000%02d", 14+i)
		b := &config.Bank{State: state, ID: id, Name: "Bank " + strings.ToUpper(id[5:]), Type: "commercial", NIPCode: code, NUBANCode: code[3:], HistoryDays: 730, Port: ports[5+2*i], APIPort: ports[6+2*i], Database: config.DatabaseName(dir, id)}
		if err := cfg.EnsureBankDir(b); err != nil {
			t.Fatal(err)
		}
		s.banks[id] = s.seed(b)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "banks" && !(withNIBSS && e.Name() == config.NIBSSID) {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
	if withNIBSS {
		if s.token, err = config.ReadSecret(filepath.Join(config.NIBSSDir(dir), config.NIBSSTokenFile)); err != nil {
			t.Fatal(err)
		}
		s.run(config.NIBSSID, s.control+"/v1/health", s.token)
	}
	for _, id := range ids {
		s.start(id)
	}
	return s
}

func (s *solo) seed(b *config.Bank) *soloBank {
	t, ctx := s.t, s.ctx
	st, err := store.Open(ctx, s.dbURL, b.Database, openbanking.Migrate)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Drop(ctx) })
	now := clock.New().Now()
	books := bank.New(ledger.New(st.Pool, nil), bank.Profile{ID: b.ID, Name: b.Name, NIPCode: b.NIPCode, NUBANCode: b.NUBANCode}, banktest.Fees(t))
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
	if cost, err := bcrypt.Cost([]byte(hash)); err != nil || cost != bcrypt.DefaultCost {
		t.Fatalf("password hash cost %d (%v), want %d", cost, err, bcrypt.DefaultCost)
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
	return &soloBank{cfg: b, books: books, nuban: account.NUBAN, owner: account.Owner, site: fmt.Sprintf("http://127.0.0.1:%d", b.Port), api: fmt.Sprintf("http://127.0.0.1:%d", b.APIPort)}
}

func (s *solo) run(id, healthURL, token string) (*lockedBuffer, func()) {
	t := s.t
	t.Helper()
	c := newCLIAt(t, s.dir)
	cmd := exec.Command(c.bin, "run", "--participant", id, "--dir", s.dir)
	cmd.Env = c.env(config.DatabaseURLEnv + "=" + s.dbURL)
	logs := &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-exited:
			case <-time.After(soloStop):
				_ = cmd.Process.Kill()
				<-exited
			}
		})
	}
	t.Cleanup(stop)
	waitFor(t, waitLimit, id+" to answer on its own", func() bool {
		select {
		case err := <-exited:
			t.Fatalf("%s exited: %v\n%s", id, err, logs.String())
		default:
		}
		status, _, _ := s.call(s.plain, http.MethodGet, healthURL, token, "", nil)
		return status == http.StatusOK
	})
	return logs, stop
}

func (s *solo) start(id string) {
	b := s.banks[id]
	b.logs, b.stop = s.run(id, b.site+"/health", "")
	token, err := config.ReadSecret(filepath.Join(config.BankDir(s.dir, id), config.BankTokenFile))
	if err != nil {
		s.t.Fatal(err)
	}
	b.token = token
}

func (s *solo) call(client *http.Client, method, url, token, body string, header http.Header) (int, http.Header, string) {
	s.t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		s.t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err.Error()
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		s.t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, requestIDText.ReplaceAllString(strings.TrimSpace(string(raw)), `"request_id":"req_ID"`)
}

type soloRow struct {
	name     string
	client   *http.Client
	method   string
	url      string
	token    string
	body     string
	key      string
	status   int
	replayed bool
	want     string
}

func (s *solo) check(rows []soloRow) {
	s.t.Helper()
	for _, r := range rows {
		header := http.Header{}
		if r.key != "" {
			header.Set("Idempotency-Key", r.key)
		}
		status, h, got := s.call(r.client, r.method, r.url, r.token, r.body, header)
		got = stampText.ReplaceAllString(got, `"$1":"TIME"`)
		if replayed := h.Get("Idempotent-Replayed") == "true"; status != r.status || replayed != r.replayed || got != r.want {
			s.t.Errorf("%s: got %d replayed=%v %s, want %d replayed=%v %s", r.name, status, replayed, got, r.status, r.replayed, r.want)
		}
	}
}

func (s *solo) balance(id string) int64 {
	s.t.Helper()
	b := s.banks[id]
	got, err := b.books.L.Balance(s.ctx, bank.CustomerAccount(b.nuban))
	if err != nil {
		s.t.Fatal(err)
	}
	return got
}

func (s *solo) register(id string, limit int64) {
	s.t.Helper()
	b := s.banks[id].cfg
	body := fmt.Sprintf(`{"nip_code":%q,"nuban_code":%q,"name":%q,"type":"commercial","credit_url":"http://%s.localhost:%d","caller_id":%q,"net_debit_cap_minor":%d,"collateral_minor":0}`, b.NIPCode, b.NUBANCode, b.Name, id, b.APIPort, id, limit)
	if status, _, got := s.call(s.plain, http.MethodPut, s.control+"/v1/control/banks/"+id, s.token, body, nil); status != http.StatusOK {
		s.t.Fatalf("register %s: %d %s", id, status, got)
	}
}

func (s *solo) bankCall(id, method, path, key, body string) (int, http.Header, map[string]any) {
	s.t.Helper()
	header := http.Header{}
	if key != "" {
		header.Set("Idempotency-Key", key)
	}
	b := s.banks[id]
	status, h, raw := s.call(s.plain, method, b.site+path, b.token, body, header)
	out := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		s.t.Fatalf("%s %s answered %d %s", method, path, status, raw)
	}
	return status, h, out
}

func (s *solo) fault(id, body string) {
	s.t.Helper()
	if status, _, got := s.bankCall(id, http.MethodPost, "/v1/faults", "fault-"+body, body); status != http.StatusCreated {
		s.t.Fatalf("fault at %s: %d %v", id, status, got)
	}
}

func (s *solo) send(key string, amount int64) (int, http.Header, map[string]any) {
	body := fmt.Sprintf(`{"from":%q,"to_bank":"bank-b","to":%q,"amount":"%d.%02d","narration":"Rent"}`, s.banks["bank-a"].nuban, s.banks["bank-b"].nuban, amount/100, amount%100)
	return s.bankCall("bank-a", http.MethodPost, "/v1/transfers", key, body)
}

func (s *solo) query(sid string) map[string]any {
	_, _, got := s.bankCall("bank-a", http.MethodPost, "/v1/nip/sends/"+sid+"/status-query", "", "")
	return got
}

func (s *solo) sessionBody(sid string, amount int64) string {
	return fmt.Sprintf(`{"session_id":%q,"from_code":"000014","to_code":"000015","from_nuban":%q,"to_nuban":%q,"from_name":"ADA OBI","amount_minor":%d,"narration":"Rent","closure":false}`, sid, s.banks["bank-a"].nuban, s.banks["bank-b"].nuban, amount)
}

func dirListing(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == root {
			return err
		}
		rel, err := filepath.Rel(root, path)
		out = append(out, rel)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

func (s *solo) onlyFiles(want []string, modes ...string) {
	s.t.Helper()
	if got := dirListing(s.t, s.dir); !slices.Equal(got, want) {
		s.t.Errorf("the directory holds %v, want %v", got, want)
	}
	for _, m := range modes {
		if fi, err := os.Stat(filepath.Join(s.dir, m)); err != nil || fi.Mode().Perm() != 0o600 {
			s.t.Errorf("%s: mode %v, error %v, want 0600", m, fi, err)
		}
	}
}

func TestSoloBank(t *testing.T) {
	t.Parallel()
	t.Run("serves with nothing else running", func(t *testing.T) {
		s := newSolo(t, false, time.Minute, "", "bank-a")
		a := s.banks["bank-a"]
		nibssClient := callerClient(t, config.NIBSSID)
		session := func(n int) string { return banktest.Session(a.cfg.NIPCode, n) }
		reply := func(sid, code string) string { return fmt.Sprintf(`{"session_id":%q,"response_code":%q}`, sid, code) }
		credit := func(n int, amount int64) string { return banktest.CreditBody(session(n), a.nuban, amount, "Rent") }
		nip := []struct {
			soloRow
			balance int64
		}{
			{soloRow{name: "name enquiry", method: "GET", url: "/v1/nip/account-names/" + a.nuban, want: fmt.Sprintf(`{"response_code":"00","account_number":%q,"account_name":"ADA OBI"}`, a.nuban)}, soloDeposit},
			{soloRow{name: "credit", method: "POST", url: "/v1/nip/credits", body: credit(1, soloAmount), want: reply(session(1), "00")}, soloDeposit + soloAmount},
			{soloRow{name: "credit replay", method: "POST", url: "/v1/nip/credits", body: credit(1, soloAmount), replayed: true, want: reply(session(1), "00")}, soloDeposit + soloAmount},
			{soloRow{name: "status of a credit never made", method: "GET", url: "/v1/nip/credits/" + session(2), want: reply(session(2), "25")}, soloDeposit + soloAmount},
			{soloRow{name: "credit after the status query found nothing", method: "POST", url: "/v1/nip/credits", body: credit(2, 1_000_000), want: reply(session(2), "25")}, soloDeposit + soloAmount},
		}
		for _, r := range nip {
			r.client, r.url, r.status = nibssClient, a.api+r.url, http.StatusOK
			s.check([]soloRow{r.soloRow})
			if got := s.balance("bank-a"); got != r.balance {
				t.Fatalf("%s: balance %d, want %d", r.name, got, r.balance)
			}
		}
		if status, _, _ := s.call(s.plain, http.MethodPost, a.site+"/v1/customers", a.token, `{"name":"Bola Ade","phone":"08030000001","dob":"1991-01-01"}`, http.Header{"Idempotency-Key": {"cus-bola"}}); status != http.StatusServiceUnavailable {
			t.Errorf("a customer needs a BVN from core, which is down; got %d, want 503", status)
		}
		jar, err := cookiejar.New(nil)
		if err != nil {
			t.Fatal(err)
		}
		browser := &http.Client{Timeout: 30 * time.Second, Jar: jar}
		login := url.Values{"username": {"ada"}, "password": {soloPassword}}
		if resp, err := browser.PostForm(a.site+"/login", login); err != nil {
			t.Fatal(err)
		} else {
			resp.Body.Close()
		}
		status, _, body := s.call(browser, http.MethodGet, a.site+"/transfer", "", "", nil)
		if want := "Bank A cannot reach NIBSS right now. Try again in a minute."; status != http.StatusServiceUnavailable || body != want {
			t.Errorf("the transfer page answered %d %q, want 503 %q", status, body, want)
		}
		own := "banks/bank-a/"
		s.onlyFiles([]string{"banks", "banks/bank-a", own + "bank.json", own + "control-token"}, own+"bank.json", own+"control-token")
		conn, err := pgx.Connect(s.ctx, s.dbURL)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(s.ctx)
		prefix := strings.TrimSuffix(config.DatabaseName(s.dir, "x"), "x")
		rows, err := conn.Query(s.ctx, `SELECT DISTINCT datname FROM pg_stat_activity WHERE datname LIKE $1 ORDER BY datname`, strings.ReplaceAll(prefix, "_", `\_`)+"%")
		if err != nil {
			t.Fatal(err)
		}
		if opened, err := pgx.CollectRows(rows, pgx.RowTo[string]); err != nil || !slices.Equal(opened, []string{a.cfg.Database}) {
			t.Errorf("databases open: %v (%v), want only %s", opened, err, a.cfg.Database)
		}

		nuban := a.nuban
		account := func(status string, balance int64) string {
			return fmt.Sprintf(`{"nuban":%q,"name":"ADA OBI","customer":%q,"type":"current","status":%q,"currency":"NGN","balance_minor":%d}`, nuban, a.owner, status, balance)
		}
		payout := func(amount int64) string {
			return fmt.Sprintf(`{"account_number":%q,"amount_minor":%d}`, nuban, amount)
		}
		notClosing := soloErr("bank_not_closing", "Bank A is active, so it cannot pay out or freeze accounts; that happens only while a bank is closing.")
		const paid = 1_000_000
		balance := int64(soloDeposit + soloAmount)
		admin := func(name, method, path, body, key string, status int, replayed bool, want string) soloRow {
			return soloRow{name: name, client: s.plain, method: method, url: a.site + path, token: a.token, body: body, key: key, status: status, replayed: replayed, want: want}
		}
		s.check([]soloRow{
			admin("read the account", "GET", "/v1/accounts/"+nuban, "", "", 200, false, account("active", balance)),
			admin("an unknown account", "GET", "/v1/accounts/0000000000", "", "", 404, false, soloErr("not_found", "No such account.")),
			admin("a malformed BVN", "GET", "/v1/customers?bvn=229", "", "", 400, false, soloErr("invalid_request", "bvn must be 11 digits.")),
			admin("a BVN nobody has", "GET", "/v1/customers?bvn=22900000000", "", "", 200, false, `{"data":[]}`),
			admin("payout while active", "POST", "/v1/closure-payouts", payout(paid), "pay-1", 409, false, notClosing),
			admin("freeze while active", "POST", "/v1/accounts/"+nuban+"/freeze", "", "freeze-1", 409, false, notClosing),
		})
		if status, _, got := s.call(s.plain, http.MethodPut, a.site+"/v1/phase", a.token, `{"phase":"closing","reason":"Licence revoked.","generation":1,"world_id":"wld_solo"}`, nil); status != http.StatusOK {
			t.Fatalf("start closing: %d %s", status, got)
		}
		s.check([]soloRow{
			admin("payout without a key", "POST", "/v1/closure-payouts", payout(paid), "", 400, false, soloErr("invalid_request", "An Idempotency-Key header of 1 to 255 characters is required on writes.")),
			admin("payout above the balance", "POST", "/v1/closure-payouts", payout(balance+1), "pay-big", 422, false, soloErr("insufficient_funds", fmt.Sprintf("Account %s holds ₦525,000.00 and a payout of ₦525,000.01 was asked for; pay out ₦525,000.00 or less.", nuban))),
		})
		if status, _, _ := s.call(s.plain, http.MethodPost, a.site+"/v1/closure-payouts", a.token, payout(paid), http.Header{"Idempotency-Key": {"pay-1"}}); status != http.StatusCreated {
			t.Fatalf("payout: %d", status)
		}
		s.check([]soloRow{
			admin("payout replay", "POST", "/v1/closure-payouts", payout(paid), "pay-1", 201, true, `{"account_number":"`+nuban+`","amount_minor":1000000,"balance_minor":51500000,"replayed":false,"posted_at":"TIME"}`),
			admin("payout key reused", "POST", "/v1/closure-payouts", payout(paid+1), "pay-1", 422, false, soloErr("idempotency_key_reused", "This Idempotency-Key was used with a different request.")),
			admin("freeze", "POST", "/v1/accounts/"+nuban+"/freeze", "", "freeze-1", 200, false, account("frozen", balance-paid)),
			admin("freeze replay", "POST", "/v1/accounts/"+nuban+"/freeze", "", "freeze-1", 200, true, account("frozen", balance-paid)),
			admin("the ledger check", "GET", "/v1/ledger/check", "", "", 200, false, `{"ok":true,"trial_balance_minor":0,"balance_mismatches":[],"event_mismatches":[],"unbalanced_entries":[]}`),
		})
		if got := s.balance("bank-a"); got != balance-paid {
			t.Errorf("balance %d, want %d", got, balance-paid)
		}
	})

	t.Run("a closed bank refuses everything", func(t *testing.T) {
		s := newSolo(t, false, time.Minute, "closed", "bank-a")
		a := s.banks["bank-a"]
		closed := func(name, method, url, body string, client *http.Client, want string) soloRow {
			return soloRow{name: name, client: client, method: method, url: url, body: body, status: http.StatusConflict, want: want}
		}
		got := func(r soloRow) (int, string) {
			status, _, text := s.call(r.client, r.method, r.url, "", r.body, nil)
			return status, text
		}
		for _, r := range []soloRow{
			closed("nip credit", "POST", a.api+"/v1/nip/credits", banktest.CreditBody(banktest.Session(a.cfg.NIPCode, 1), a.nuban, soloAmount, "Rent"), callerClient(t, config.NIBSSID), soloErr("bank_closed", "Bank A is closed.")),
			closed("site", "GET", a.site+"/login", "", s.plain, "Bank A is closed."),
		} {
			if status, text := got(r); status != r.status || text != r.want {
				t.Errorf("%s: got %d %s, want %d %s", r.name, status, text, r.status, r.want)
			}
		}
		if got := s.balance("bank-a"); got != soloDeposit {
			t.Errorf("balance %d, want %d: a closed bank credited something", got, soloDeposit)
		}
	})
}

func TestSoloNIBSS(t *testing.T) {
	t.Parallel()
	t.Run("directory, names and consents", func(t *testing.T) {
		s := newSolo(t, true, time.Minute, "", "bank-a", "bank-b")
		s.register("bank-a", soloCap)
		s.register("bank-b", soloCap)
		a, b := s.banks["bank-a"], s.banks["bank-b"]
		bankURL := s.control + "/v1/control/banks/bank-a"
		entry := func(status string) string {
			return fmt.Sprintf(`{"id":"bank-a","name":"Bank A","nip_code":"000014","nuban_code":"014","type":"commercial","status":%q}`, status)
		}
		registered := fmt.Sprintf(`{"nip_code":"000014","nuban_code":"014","name":"Bank A","type":"commercial","credit_url":"http://bank-a.localhost:%d","caller_id":"bank-a","net_debit_cap_minor":%d,"collateral_minor":0}`, a.cfg.APIPort, soloCap)
		moved := strings.Replace(registered, fmt.Sprint(a.cfg.APIPort), "17499", 1)
		unauthenticated := soloErr("unauthenticated", "A valid control token is required.")
		enquiry := func(code, nuban string) string {
			return s.dirURL + "/v1/name-enquiries?bank_code=" + code + "&account_number=" + nuban
		}
		answer := func(code, message, name, nuban string) string {
			out := fmt.Sprintf(`{"response_code":%q,"message":%q,"bank_code":"000014","bank_name":"Bank A","account_number":%q`, code, message, nuban)
			if name != "" {
				out += fmt.Sprintf(`,"account_name":%q`, name)
			}
			return out + "}"
		}
		badDigit := a.nuban[:9] + string('0'+(a.nuban[9]-'0'+1)%10)
		event := func(seq int, id, status string) string {
			return fmt.Sprintf(`{"incarnation":"db1","source_seq":%d,"consent_id":%q,"client_id":"OBR-1","partner_id":"balance","status":%q,"via":"bank","changed_at":"2026-11-14T10:30:00+01:00"}`, seq, id, status)
		}
		recorded := func(seq int, bank string, source int, id, status string) string {
			return fmt.Sprintf(`{"seq":%d,"bank":%q,"incarnation":"db1","consent_id":%q,"client_id":"OBR-1","partner_id":"balance","status":%q,"via":"bank","changed_at":"TIME","source_seq":%d}`, seq, bank, id, status, source)
		}
		consents := s.dirURL + "/v1/consents"
		ledgerURL := s.control + "/v1/control/consents"
		s.check([]soloRow{
			{name: "register again", client: s.plain, method: "PUT", url: bankURL, token: s.token, body: registered, status: 200, want: entry("active")},
			{name: "register with another url", client: s.plain, method: "PUT", url: bankURL, token: s.token, body: moved, status: 422, want: soloErr("registration_conflict", fmt.Sprintf("Bank bank-a is registered with credit_url http://bank-a.localhost:%d, and this request has credit_url http://bank-a.localhost:17499. Send the registered details.", a.cfg.APIPort))},
			{name: "register without a token", client: s.plain, method: "PUT", url: bankURL, body: registered, status: 401, want: unauthenticated},
			{name: "directory for a stranger", client: s.clients["bank-z"], method: "GET", url: s.dirURL + "/v1/banks", status: 403, want: soloErr("forbidden", "bank-z may not read the bank directory. It is not a bank NIBSS knows.")},
			{name: "real name", client: s.clients["bank-a"], method: "GET", url: enquiry("000014", a.nuban), status: 200, want: answer("00", "Successful.", "ADA OBI", a.nuban)},
			{name: "bad check digit", client: s.clients["bank-a"], method: "GET", url: enquiry("000014", badDigit), status: 200, want: answer("07", "That is not a valid account number at Bank A.", "", badDigit)},
			{name: "short number", client: s.clients["bank-a"], method: "GET", url: enquiry("000014", "123456789"), status: 400, want: soloErr("invalid_request", "account_number must have 10 digits.")},
			{name: "unknown account", client: s.clients["bank-a"], method: "GET", url: enquiry("000014", world.NUBAN("014", 99)), status: 200, want: answer("25", "No such account at Bank A.", "", world.NUBAN("014", 99))},
			{name: "stranger asks for a name", client: s.clients["bank-z"], method: "GET", url: enquiry("000014", a.nuban), status: 403, want: soloErr("forbidden", "bank-z may not ask for account names. It is not a bank NIBSS knows.")},
			{name: "record a consent", client: s.clients["bank-a"], method: "POST", url: consents, body: event(1, "obc_1", "Authorised"), status: 200, want: recorded(1, "bank-a", 1, "obc_1", "Authorised")},
			{name: "record it again", client: s.clients["bank-a"], method: "POST", url: consents, body: event(1, "obc_1", "Authorised"), status: 200, replayed: true, want: recorded(1, "bank-a", 1, "obc_1", "Authorised")},
			{name: "the source seq with another status", client: s.clients["bank-a"], method: "POST", url: consents, body: event(1, "obc_1", "Revoked"), status: 422, want: soloErr("idempotency_key_reused", "Source sequence 1 of bank bank-a was first recorded with other status. Send a new source_seq, or repeat the first request.")},
			{name: "a stranger records a consent", client: s.clients["bank-z"], method: "POST", url: consents, body: event(1, "obc_1", "Authorised"), status: 403, want: soloErr("forbidden", "bank-z may not record consents. It is not a bank NIBSS knows.")},
			{name: "an unknown status", client: s.clients["bank-a"], method: "POST", url: consents, body: event(2, "obc_1", "Maybe"), status: 422, want: soloErr("invalid_request", `Consent obc_1 event: status "Maybe" must be AwaitingAuthorisation, Authorised, Rejected, Revoked, Expired, Dormant, ReconsentRequired.`)},
			{name: "the ledger", client: s.plain, method: "GET", url: ledgerURL, token: s.token, status: 200, want: `{"data":[` + recorded(1, "bank-a", 1, "obc_1", "Authorised") + `]}`},
			{name: "the ledger without a token", client: s.plain, method: "GET", url: ledgerURL, status: 401, want: unauthenticated},
			{name: "the ledger with a bad limit", client: s.plain, method: "GET", url: ledgerURL + "?limit=0", token: s.token, status: 400, want: soloErr("invalid_request", "limit must be a number from 1 to 500.")},
		})

		debit := int64(soloAmount) + func() int64 { c := banktest.Fees(t).ForTransfer(soloAmount); return c.Fee + c.VAT + c.StampDuty }()
		a0, b0 := s.balance("bank-a"), s.balance("bank-b")
		check := func(name string, wantA, wantB int64) {
			t.Helper()
			if gotA, gotB := s.balance("bank-a"), s.balance("bank-b"); gotA != wantA || gotB != wantB {
				t.Fatalf("%s: balances %d and %d, want %d and %d", name, gotA, gotB, wantA, wantB)
			}
		}
		status, _, sent := s.send("send-1", soloAmount)
		sid, _ := sent["session_id"].(string)
		if status != 200 || sent["status"] != "success" || sent["response_code"] != "00" || sent["total_debit_minor"] != float64(debit) || sent["to_name"] != "ADA OBI" || !sessionText.MatchString(sid) {
			t.Fatalf("send: %d %v", status, sent)
		}
		check("success with charges on the sender", a0-debit, b0+soloAmount)
		if status, h, again := s.send("send-1", soloAmount); status != 200 || h.Get("Idempotent-Replayed") != "true" || again["session_id"] != sid {
			t.Errorf("a replay at the bank: %d %v %v", status, h, again)
		}
		if status, h, _ := s.call(s.clients["bank-a"], http.MethodPost, s.dirURL+"/v1/transfers", "", s.sessionBody(sid, soloAmount), nil); status != 200 || h.Get("Idempotent-Replayed") != "true" {
			t.Errorf("a replay at NIBSS: %d %v", status, h)
		}
		status, _, got := s.call(s.clients["bank-a"], http.MethodPost, s.dirURL+"/v1/transfers", "", s.sessionBody(sid, 1), nil)
		if want := soloErr("idempotency_key_reused", fmt.Sprintf("Session %s was first sent with amount_minor %d, and this request has 1. Send a new session_id, or repeat the first request.", sid, soloAmount)); status != 422 || got != want {
			t.Errorf("the same session with another amount: %d %s", status, got)
		}
		check("refusals move nothing", a0-debit, b0+soloAmount)

		s.register("bank-a", soloBelowCap)
		_, _, capped := s.send("send-over-cap", soloAmount)
		s.register("bank-a", soloCap)
		if capped["status"] != "reversed" || capped["response_code"] != "61" {
			t.Errorf("over the cap: %v", capped)
		}
		if got := s.query(fmt.Sprint(capped["session_id"])); got["status"] != "61" || got["outcome"] != "failed" || got["tsq_attempts"] != float64(0) {
			t.Errorf("the capped session at NIBSS: %v", got)
		}
		check("reversed once", a0-debit, b0+soloAmount)

		s.fault("bank-b", `{"kind":"down"}`)
		if status, _, got := s.send("send-down", soloAmount); status != 503 || fmt.Sprint(got["error"].(map[string]any)["message"]) != "Bank B is not available right now, so nothing was debited." {
			t.Errorf("bank B down: %d %v", status, got)
		}
		if status, _, got := s.bankCall("bank-b", http.MethodDelete, "/v1/faults", "", ""); status != 200 {
			t.Fatalf("clear faults: %d %v", status, got)
		}
		check("refused before the debit", a0-debit, b0+soloAmount)

		s.fault("bank-b", `{"kind":"timeout","outcome":"credited","count":1}`)
		_, _, held := s.send("send-held", soloAmount)
		if held["status"] != "pending" || held["next_check_at"] == nil {
			t.Fatalf("a timeout: %v", held)
		}
		b.stop()
		for range 2 {
			if got := s.query(fmt.Sprint(held["session_id"])); got["status"] != "pending" {
				t.Fatalf("a status query with bank B stopped: %v", got)
			}
		}
		s.start("bank-b")
		if got := s.query(fmt.Sprint(held["session_id"])); got["status"] != "00" || got["outcome"] != "credited" {
			t.Fatalf("a status query after bank B came back: %v", got)
		}
		check("credited once", a0-2*debit, b0+2*soloAmount)

		s.fault("bank-b", `{"kind":"timeout","outcome":"unknown","count":3}`)
		_, _, silent := s.send("send-silent", soloAmount)
		var seen []string
		for range 4 {
			got := s.query(fmt.Sprint(silent["session_id"]))
			seen = append(seen, fmt.Sprint(got["status"], got["tsq_attempts"]))
		}
		if want := []string{"pending1", "pending2", "253", "253"}; !slices.Equal(seen, want) {
			t.Errorf("a silent bank: %v, want %v", seen, want)
		}
		check("a silent bank is reversed once", a0-2*debit, b0+2*soloAmount)

		pool := a.books.L.Pool()
		outbox := &openbanking.PGStore{Pool: func() *pgxpool.Pool { return pool }}
		now := time.Now()
		for _, status := range []openbanking.Status{openbanking.Authorised, openbanking.Revoked} {
			if err := outbox.EnqueueLedger(s.ctx, openbanking.ConsentEvent{ConsentID: "obc_outbox", Bank: "bank-a", ClientID: "OBR-1", PartnerID: "balance", Status: status, Via: "bank", At: now}, now); err != nil {
				t.Fatal(err)
			}
		}
		a.stop()
		s.start("bank-a")
		var ledgerText string
		waitFor(t, waitLimit, "the bank to flush its consents to NIBSS", func() bool {
			_, _, ledgerText = s.call(s.plain, http.MethodGet, ledgerURL+"?bank=bank-a", s.token, "", nil)
			return strings.Contains(ledgerText, `"consent_id":"obc_outbox","client_id":"OBR-1","partner_id":"balance","status":"Revoked"`) && strings.Contains(ledgerText, `"status":"Authorised"`)
		})
		var left int
		waitFor(t, waitLimit, "the bank to mark its events delivered", func() bool {
			if err := pool.QueryRow(s.ctx, `SELECT count(*) FROM ob_events WHERE delivered_at IS NULL`).Scan(&left); err != nil {
				t.Fatal(err)
			}
			return left == 0
		})

		bankB := s.control + "/v1/control/banks/bank-b/status"
		entryB := func(status string) string {
			return fmt.Sprintf(`{"id":"bank-b","name":"Bank B","nip_code":"000015","nuban_code":"015","type":"commercial","status":%q}`, status)
		}
		put := func(name, status string, code int, want string) soloRow {
			return soloRow{name: name, client: s.plain, method: "PUT", url: bankB, token: s.token, body: `{"status":"` + status + `"}`, status: code, want: want}
		}
		s.check([]soloRow{
			put("suspend", "suspended", 200, entryB("suspended")),
			{name: "a suspended bank is unavailable", client: s.clients["bank-a"], method: "GET", url: enquiry("000015", b.nuban), status: 200, want: `{"response_code":"91","message":"Bank B is not available right now.","bank_code":"000015","bank_name":"Bank B","account_number":"` + b.nuban + `"}`},
			put("remove too early", "removed", 422, soloErr("status_not_allowed", "Bank bank-b is suspended and cannot become removed. It can become active or terminating.")),
			put("unknown status", "closed", 400, soloErr("invalid_request", "The status must be active, suspended, terminating or removed.")),
			put("terminate", "terminating", 200, entryB("terminating")),
			put("remove", "removed", 200, entryB("removed")),
			{name: "the directory no longer admits it", client: s.clients["bank-b"], method: "GET", url: s.dirURL + "/v1/banks", status: 403, want: soloErr("forbidden", "bank-b may not read the bank directory. It is not a bank NIBSS knows.")},
		})
		s.onlyFiles([]string{"banks", "banks/bank-a", "banks/bank-a/bank.json", "banks/bank-a/control-token", "banks/bank-b", "banks/bank-b/bank.json", "banks/bank-b/control-token", "nibss", "nibss/control-token", "nibss/nibss.json"}, "nibss/nibss.json", "nibss/control-token")
	})

	t.Run("settlement reports reach their own bank", func(t *testing.T) {
		s := newSolo(t, true, time.Minute, "")
		s.banks["bank-a"] = &soloBank{cfg: &config.Bank{ID: "bank-a", Name: "Bank A", NIPCode: "000014", NUBANCode: "014", APIPort: 1}}
		s.banks["bank-b"] = &soloBank{cfg: &config.Bank{ID: "bank-b", Name: "Bank B", NIPCode: "000015", NUBANCode: "015", APIPort: 2}}
		s.clients["bank-a"], s.clients["bank-b"] = callerClient(t, "bank-a"), callerClient(t, "bank-b")
		s.register("bank-a", soloCap)
		s.register("bank-b", soloCap)
		db, err := nibss.Open(s.ctx, s.dbURL, config.DatabaseName(s.dir, config.NIBSSID))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		at := func(h int) time.Time { return time.Date(2026, 11, 4, h, 0, 0, 0, clock.Lagos) }
		pay := func(id, from, to string, amount int64, key string, cutoff time.Time) {
			x := nibss.Session{ID: id, FromCode: from, ToCode: to, FromNUBAN: world.NUBAN("014", 1), ToNUBAN: world.NUBAN("015", 1), Amount: amount, Narration: "rent", CreatedAt: cutoff.Add(-time.Hour)}
			if _, _, err := db.Begin(s.ctx, x); err != nil {
				t.Fatal(err)
			}
			if err := db.Resolve(s.ctx, id, nibss.SessionOK, nip.OutcomeCredited, cutoff.Add(-time.Minute)); err != nil {
				t.Fatal(err)
			}
			if err := db.MarkSettled(s.ctx, []string{id}, key, cutoff); err != nil {
				t.Fatal(err)
			}
		}
		pay("r1", "000014", "000015", 500, "2026-11-04T12:00", at(12))
		pay("r2", "000015", "000014", 200, "2026-11-04T12:00", at(12))
		pay("r3", "000014", "000015", 70, "2026-11-04T18:00", at(18))
		pay("r4", "000014", "000015", 9, "2026-11-04T23:00", at(23))
		for h, key := range map[int]string{12: "2026-11-04T12:00", 18: "2026-11-04T18:00"} {
			if err := db.MarkPosted(s.ctx, key, at(h)); err != nil {
				t.Fatal(err)
			}
		}
		row := func(key string, h int, outward, inward, net int64, transfers int) string {
			return fmt.Sprintf(`{"session_id":%q,"cutoff_at":"2026-11-04T%02d:00:00+01:00","posted_at":"TIME","outward_minor":%d,"inward_minor":%d,"net_minor":%d,"transfers":%d}`, key, h, outward, inward, net, transfers)
		}
		position := func(outward, inward int64, transfers, settlements int) string {
			return fmt.Sprintf(`{"outward_minor":%d,"inward_minor":%d,"pending_inward_minor":0,"transfers":%d,"settlements":%d}`, outward, inward, transfers, settlements)
		}
		noon, evening := row("2026-11-04T12:00", 12, 500, 200, -300, 2), row("2026-11-04T18:00", 18, 70, 0, -70, 1)
		reports, positionURL := s.dirURL+"/v1/settlement-reports", s.dirURL+"/v1/settlement-position"
		get := func(name string, client string, url string, status int, want string) soloRow {
			return soloRow{name: name, client: s.clients[client], method: "GET", url: url, status: status, want: want}
		}
		s.check([]soloRow{
			get("bank a reads its posted reports", "bank-a", reports, 200, `{"data":[`+noon+`,`+evening+`]}`),
			get("bank b reads its own", "bank-b", reports, 200, `{"data":[`+row("2026-11-04T12:00", 12, 200, 500, 300, 2)+`,`+row("2026-11-04T18:00", 18, 0, 70, 70, 1)+`]}`),
			get("after the first", "bank-a", reports+"?after=2026-11-04T12:00", 200, `{"data":[`+evening+`]}`),
			get("after a session that is not posted", "bank-a", reports+"?after=2026-11-04T23:00", 200, `{"data":[`+noon+`,`+evening+`]}`),
			get("a bad limit", "bank-a", reports+"?limit=0", 400, soloErr("invalid_request", "limit must be a number from 1 to 500.")),
			get("a stranger", "bank-z", reports, 403, soloErr("forbidden", "bank-z may not read settlement reports. It is not a bank NIBSS knows.")),
			get("bank a position", "bank-a", positionURL, 200, position(579, 200, 4, 3)),
			get("bank b position after the first", "bank-b", positionURL+"?after=2026-11-04T12:00", 200, position(0, 79, 2, 2)),
			{name: "open sessions of bank a", client: s.plain, method: "GET", url: s.control + "/v1/control/banks/bank-a/open-sessions", token: s.token, status: 200, want: `{"bank_id":"bank-a","transfers":0,"outward_minor":0,"inward_minor":0,"pending_inward_minor":0,"unposted_settlements":1}`},
		})
		_, _, got := s.call(s.plain, http.MethodGet, s.control+"/v1/status", s.token, "", nil)
		var st struct {
			Problems       []string `json:"problems"`
			PostedSessions int      `json:"posted_sessions"`
		}
		wantProblem := "settlement session 2026-11-04T23:00 was cut off at 2026-11-04T23:00:00+01:00 and is not posted to the CBN yet"
		if err := json.Unmarshal([]byte(got), &st); err != nil || len(st.Problems) != 1 || st.Problems[0] != wantProblem || st.PostedSessions != 2 {
			t.Errorf("status: %s (%v), want the problem %q and 2 posted sessions", got, err, wantProblem)
		}
	})
}
