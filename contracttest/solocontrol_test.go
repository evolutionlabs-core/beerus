package contracttest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

func TestSoloBankServesItsAdminRoutes(t *testing.T) {
	s := startSoloBank(t, "")
	spec := specValidator(t, participantAPI)
	token, err := config.ReadSecret(filepath.Join(config.BankDir(s.solo, s.own.ID), config.BankTokenFile))
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body, key string) (int, http.Header, string) {
		t.Helper()
		build := func() *http.Request {
			req, err := http.NewRequest(method, s.site+path, strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+token)
			if body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			if key != "" {
				req.Header.Set("Idempotency-Key", key)
			}
			return req
		}
		resp, err := s.probe.plain.Do(build())
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		check := &http.Response{StatusCode: resp.StatusCode, Header: resp.Header, Body: io.NopCloser(bytes.NewReader(raw))}
		if ok, errs := spec.ValidateHttpResponse(build(), check); !ok {
			t.Fatalf("%s %s answered %d %s, which breaks the participant contract: %v", method, path, resp.StatusCode, raw, errs)
		}
		return resp.StatusCode, resp.Header, requestIDText.ReplaceAllString(strings.TrimSpace(string(raw)), `"request_id":"req_ID"`)
	}
	errBody := func(code, message string) string {
		return fmt.Sprintf(`{"error":{"code":%q,"message":%q,"request_id":"req_ID"}}`, code, message)
	}
	nuban := s.account.NUBAN
	owner := s.account.Owner
	account := func(status string, balance int64) string {
		return fmt.Sprintf(`{"nuban":%q,"name":"ADA OBI","customer":%q,"type":"current","status":%q,"currency":"NGN","balance_minor":%d}`, nuban, owner, status, balance)
	}
	payout := func(amount int64) string {
		return fmt.Sprintf(`{"account_number":%q,"amount_minor":%d}`, nuban, amount)
	}
	const paid = 1_000_000
	notClosing := func(phase string) string {
		return errBody("bank_not_closing", "Bank A is "+phase+", so it cannot pay out or freeze accounts; that happens only while a bank is closing.")
	}
	rows := []struct {
		name     string
		method   string
		path     string
		body     string
		key      string
		status   int
		replayed bool
		want     string
	}{
		{"read the account", "GET", "/v1/accounts/" + nuban, "", "", 200, false, account("active", soloDeposit)},
		{"read an unknown account", "GET", "/v1/accounts/0000000000", "", "", 404, false, errBody("not_found", "No such account.")},
		{"find the customer by BVN", "GET", "/v1/customers?bvn=22912345678", "", "", 200, false, ""},
		{"a malformed BVN", "GET", "/v1/customers?bvn=229", "", "", 400, false, errBody("invalid_request", "bvn must be 11 digits.")},
		{"a BVN nobody has", "GET", "/v1/customers?bvn=22900000000", "", "", 200, false, `{"data":[]}`},
		{"totals", "GET", "/v1/books/totals", "", "", 200, false, fmt.Sprintf(`{"customers":1,"accounts":1,"deposits_minor":%d}`, soloDeposit)},
		{"entries", "GET", "/v1/books/accounts/" + nuban + "/entries", "", "", 200, false, ""},
		{"the ledger check", "GET", "/v1/ledger/check", "", "", 200, false, `{"ok":true,"trial_balance_minor":0,"balance_mismatches":[],"event_mismatches":[],"unbalanced_entries":[]}`},
		{"a right login", "POST", "/v1/logins", `{"username":"ada","password":"` + soloPassword + `"}`, "", 200, false, fmt.Sprintf(`{"customer_id":%q}`, owner)},
		{"a wrong login", "POST", "/v1/logins", `{"username":"ada","password":"wrong-password"}`, "", 422, false, errBody("invalid_login", "The username or password is wrong.")},
		{"payout while active", "POST", "/v1/closure-payouts", payout(paid), "pay-1", 409, false, notClosing("active")},
		{"freeze while active", "POST", "/v1/accounts/" + nuban + "/freeze", "", "freeze-1", 409, false, notClosing("active")},
		{"the bank starts closing", "PUT", "/v1/phase", `{"phase":"closing","reason":"Licence revoked.","generation":1,"world_id":"wld_solo"}`, "", 200, false, ""},
		{"payout without a key", "POST", "/v1/closure-payouts", payout(paid), "", 400, false, errBody("invalid_request", "An Idempotency-Key header of 1 to 255 characters is required on writes.")},
		{"payout above the balance", "POST", "/v1/closure-payouts", payout(soloDeposit + 1), "pay-big", 422, false, errBody("insufficient_funds", fmt.Sprintf("Account %s holds ₦500,000.00 and a payout of ₦500,000.01 was asked for; pay out ₦500,000.00 or less.", nuban))},
		{"payout", "POST", "/v1/closure-payouts", payout(paid), "pay-1", 201, false, ""},
		{"payout replay", "POST", "/v1/closure-payouts", payout(paid), "pay-1", 201, true, ""},
		{"payout key reused", "POST", "/v1/closure-payouts", payout(paid + 1), "pay-1", 422, false, errBody("idempotency_key_reused", "This Idempotency-Key was used with a different request.")},
		{"what was paid", "GET", "/v1/closure-payouts", "", "", 200, false, fmt.Sprintf(`{"data":[{"account_number":%q,"paid_minor":%d}]}`, nuban, paid)},
		{"freeze", "POST", "/v1/accounts/" + nuban + "/freeze", "", "freeze-1", 200, false, account("frozen", soloDeposit-paid)},
		{"freeze replay", "POST", "/v1/accounts/" + nuban + "/freeze", "", "freeze-1", 200, true, account("frozen", soloDeposit-paid)},
		{"freeze with a new key", "POST", "/v1/accounts/" + nuban + "/freeze", "", "freeze-2", 200, false, account("frozen", soloDeposit-paid)},
		{"the ledger check after the payout", "GET", "/v1/ledger/check", "", "", 200, false, `{"ok":true,"trial_balance_minor":0,"balance_mismatches":[],"event_mismatches":[],"unbalanced_entries":[]}`},
	}
	for _, r := range rows {
		status, header, got := call(r.method, r.path, r.body, r.key)
		replayed := header.Get("Idempotent-Replayed") == "true"
		if status != r.status || replayed != r.replayed || (r.want != "" && got != r.want) {
			t.Fatalf("%s: got %d replayed=%v %s, want %d replayed=%v %s", r.name, status, replayed, got, r.status, r.replayed, r.want)
		}
		if r.want == "" && r.status < 300 && r.method == "GET" {
			var page struct {
				Data []map[string]any `json:"data"`
			}
			if err := json.Unmarshal([]byte(got), &page); err != nil || len(page.Data) == 0 {
				t.Fatalf("%s: no rows in %s (%v)", r.name, got, err)
			}
		}
	}
	if got, err := s.books.L.Balance(s.ctx, bank.CustomerAccount(nuban)); err != nil || got != soloDeposit-paid {
		t.Fatalf("balance %d (%v), want %d", got, err, soloDeposit-paid)
	}
	status, _, got := call("GET", "/v1/status", "", "")
	var st struct {
		Phase string `json:"phase"`
		Steps []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Done bool   `json:"done"`
		} `json:"steps"`
	}
	if err := json.Unmarshal([]byte(got), &st); err != nil || status != 200 || st.Phase != "closing" {
		t.Fatalf("status %d %s (%v)", status, got, err)
	}
	for _, step := range st.Steps {
		if !step.Done {
			t.Fatalf("step %s is not done: %s", step.ID, got)
		}
		if step.ID == "ledger_schema" && step.Name != "Migrating bank ledger (no new migrations)" {
			t.Fatalf("the ledger step is %q", step.Name)
		}
	}
}
