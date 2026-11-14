package connect

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/world"
)

var (
	stampedField = regexp.MustCompile(`"(created_at|posted_at)":"[^"]*"`)
	accountNo    = regexp.MustCompile(`"nuban":"(\d{10})"`)
)

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

func TestParticipantBankContract(t *testing.T) {
	fixed := Options{Real: func() time.Time { return participantNow }, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	core := newTestApp(t, fixed)
	coreURL := startCore(t, core)
	o := fixed
	o.Role, o.BankID, o.CoreURL, o.Bind = RoleBank, "bank-a", coreURL, "127.0.0.1"
	a, err := New(core.cfg, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.closeLedgers)
	tok := a.ctlToken
	call := newContractCall(t, a.participantAPI())
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
	adaID := world.ID("cus", "bank-a", "api:cus-ada")
	bolaID := world.ID("cus", "bank-a", "api:cus-bola")
	adaJSON := fmt.Sprintf(`{"id":%q,"name":"Ada Obi","phone":"2348031234567","bvn":"22900000013","status":"active","username":"ada","dob":"1990-04-21","created_at":"T"}`, adaID)
	bolaJSON := fmt.Sprintf(`{"id":%q,"name":"Bola Ade","phone":"2348039876543","bvn":"22900000039","status":"active","dob":"1985-02-03","created_at":"T"}`, bolaID)
	adaBody := `{"name":"Ada Obi","phone":"08031234567","dob":"1990-04-21","username":"ada","password":"correct horse battery"}`
	step("create customer", "POST", "/v1/customers", "cus-ada", adaBody, 201, adaJSON)
	if got := call("POST", "/v1/customers", "cus-ada", adaBody, tok, true); !got.replayed || norm(got.body) != adaJSON {
		t.Errorf("customer replay: %+v", got)
	}
	step("customer key reused", "POST", "/v1/customers", "cus-ada", strings.Replace(adaBody, "Ada Obi", "Ada Obi Jr", 1), 422, apiErr("idempotency_key_reused", "This Idempotency-Key was used with a different request."))
	refused("customer without a key", "POST", "/v1/customers", "", adaBody, 400, apiErr("invalid_request", "An Idempotency-Key header of 1 to 255 characters is required on writes."))
	step("customer with a bad phone", "POST", "/v1/customers", "cus-bad", `{"name":"Bo","phone":"123","dob":"1990-04-21"}`, 422, apiErr("invalid_request", `Phone "123" must start with +234 or 0.`))
	refused("customer with a bad date", "POST", "/v1/customers", "cus-date", `{"name":"Bo","phone":"08031234567","dob":"21/04/1990"}`, 422, apiErr("invalid_request", `Dob "21/04/1990" must be a date like 1990-04-21.`))
	refused("customer with an unknown field", "POST", "/v1/customers", "cus-field", `{"name":"Bo","age":3}`, 400, apiErr("invalid_request", `Send {"name":"Ada Obi","phone":"08031234567","dob":"1990-04-21"}, with optional username and password.`))
	step("username taken", "POST", "/v1/customers", "cus-twin", `{"name":"Ada Twin","phone":"08030000001","dob":"1991-01-01","username":"ada","password":"another passphrase"}`, 409, apiErr("username_taken", "Username is taken: ada."))
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
	line := func(seq, amount, after int64, narration string) string {
		return fmt.Sprintf(`{"seq":%d,"amount_minor":%d,"balance_after_minor":%d,"posted_at":"T","value_date":"2026-11-14","narration":%q}`, seq, amount, after, narration)
	}
	deposited, withdrew := line(1, 500000, 500000, "CASH DEPOSIT"), line(2, -120000, 380000, "CASH WITHDRAWAL")
	step("statement", "GET", "/v1/accounts/"+adaAcct+"/statement", "", "", 200, fmt.Sprintf(`{"data":[%s,%s]}`, deposited, withdrew))
	step("statement first page", "GET", "/v1/accounts/"+adaAcct+"/statement?limit=1", "", "", 200, fmt.Sprintf(`{"data":[%s],"next_cursor":"1"}`, deposited))
	step("statement last page", "GET", "/v1/accounts/"+adaAcct+"/statement?limit=1&cursor=1&from=2026-11-14&to=2026-11-14", "", "", 200, fmt.Sprintf(`{"data":[%s]}`, withdrew))
	step("statement of another day", "GET", "/v1/accounts/"+adaAcct+"/statement?from=2026-11-15", "", "", 200, `{"data":[]}`)
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
	enrol := core.nibss
	core.mu.Lock()
	core.nibss, core.nibssOpening = nil, true
	core.mu.Unlock()
	step("customer while NIBSS is closed", "POST", "/v1/customers", "cus-late", `{"name":"Chi Eze","phone":"08030000002","dob":"1992-02-02"}`, 503, apiErr("unavailable", "NIBSS is not open yet, so no BVN can be enrolled; try again soon."))
	core.mu.Lock()
	core.nibss, core.nibssOpening = enrol, false
	core.mu.Unlock()
	if got := call("PUT", "/v1/phase", "", `{"phase":"suspended","reason":"Suspended by the operator.","generation":2,"world_id":"wld_1"}`, tok, true); got.status != 200 {
		t.Fatalf("suspend: %d %s", got.status, got.body)
	}
	step("deposit while suspended", "POST", "/v1/deposits", "cash-6", deposit, 409, apiErr("bank_not_active", "Bank A is suspended, so it cannot do this now."))
	step("read while suspended", "GET", "/v1/accounts?customer="+bolaID, "", "", 200, `{"data":[]}`)
}
