package contracttest

import (
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func TestApplyFromTheSpec(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newBankStack(t)
	applied := func() setup.Spec { return storedStatus(t, s.cfg.Dir).Applied }
	post := func(path string, spec setup.Spec, extra map[string]any) (map[string]any, error) {
		raw, err := setup.Export(spec)
		if err != nil {
			t.Fatal(err)
		}
		body := map[string]any{"file": "setup.yaml", "spec": string(raw)}
		maps.Copy(body, extra)
		return s.ctlTry("POST", path, body)
	}
	generations := func() map[string]int64 {
		out := map[string]int64{}
		for _, r := range storedStatus(t, s.cfg.Dir).Resources {
			out[r.Kind+"/"+r.ID] = r.Generation
		}
		return out
	}
	settled := func(kind, id string) map[string]any {
		return s.waitResource(kind, id, 60*time.Second, func(r map[string]any) bool {
			return r != nil && r["phase"] == setup.PhaseActive && r["observed_generation"] == r["generation"]
		})
	}

	before, sum := generations(), storedStatus(t, s.cfg.Dir).AppliedSHA256
	for _, path := range []string{"/plan", "/apply"} {
		out, err := post(path, applied(), nil)
		if err != nil || out["text"] != "No changes.\n" || out["changed"] != false {
			t.Fatalf("%s of the applied spec: %v %v", path, out, err)
		}
	}
	if after := generations(); !maps.Equal(before, after) || storedStatus(t, s.cfg.Dir).AppliedSHA256 != sum {
		t.Fatalf("a repeat apply moved generations %v to %v or the applied sum", before, after)
	}

	locked := applied()
	locked.Spec.Banks[0].NUBANCode = "777"
	if _, err := post("/apply", locked, nil); err == nil || !strings.Contains(err.Error(), "HTTP 422") || !strings.Contains(err.Error(), `"code":"invalid_spec"`) || !regexp.MustCompile(`setup\.yaml:\d+:\d+: banks\[bank-a\]\.nuban_code cannot change`).MatchString(err.Error()) {
		t.Fatalf("a locked field: %v", err)
	}

	withC := applied()
	withC.Spec.Banks = append(withC.Spec.Banks, setup.Bank{ID: "bank-c", Name: "Bank C", Type: "digital", SettlesThrough: "bank-a"})
	plan, err := post("/plan", withC, nil)
	if err != nil || !strings.Contains(fmt.Sprint(plan["text"]), "+ bank bank-c") || !strings.Contains(fmt.Sprint(plan["text"]), "NIP 999003 (allocated), NUBAN 903 (allocated)") {
		t.Fatalf("plan to add a bank: %v %v", plan, err)
	}
	if got := generations(); !maps.Equal(before, got) {
		t.Fatalf("a plan moved generations: %v", got)
	}
	if _, err := post("/apply", withC, nil); err != nil {
		t.Fatal(err)
	}
	r := settled("bank", "bank-c")
	if pinned := applied().Spec.Banks[2]; pinned.NIPCode != "999003" || pinned.NUBANCode != "903" || r["generation"] != float64(1) {
		t.Fatalf("bank-c after the apply: %+v %v", pinned, r)
	}
	who := s.join("adaeze", "bank-c")
	nuban := s.openAccount(who, "current")
	enquiry := func() map[string]any {
		return s.ctl("GET", "/nip/name-enquiry?bank_code=999003&account_number="+nuban, nil)
	}
	if ne := enquiry(); ne["response_code"] != "00" || ne["account_name"] != who.Name || ne["bank_name"] != "Bank C" {
		t.Fatalf("name enquiry at the new bank: %v", ne)
	}
	s.pay("bank-a", s.persona("adaeze").Accounts[0], "bank-c", nuban, "100")

	renamed := applied()
	renamed.Spec.Banks[2].Name = "Abuja Digital"
	if _, err := post("/apply", renamed, nil); err != nil {
		t.Fatal(err)
	}
	r = settled("bank", "bank-c")
	if r["generation"] != float64(2) {
		t.Fatalf("a rename should bump the generation once: %v", r)
	}
	if ne := enquiry(); ne["bank_name"] != "Abuja Digital" || ne["response_code"] != "00" {
		t.Fatalf("name enquiry after the rename: %v", ne)
	}
	resp, err := http.Get(s.cfg.Bank("bank-c").URL() + "/login")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(page), "Abuja Digital") {
		t.Fatalf("the bank site still shows the old name: %.300s", page)
	}
	statement := fmt.Sprint(s.ctl("GET", "/banks/bank-a/accounts/"+s.persona("adaeze").Accounts[0]+"/statement?limit=100", nil)["data"])
	if !strings.Contains(statement, "Bank C") || strings.Contains(statement, "Abuja Digital") {
		t.Fatalf("the sender's statement should keep the name the bank had: %s", statement)
	}

	byeA := applied()
	byeA.Spec.Banks = byeA.Spec.Banks[1:]
	if _, err := post("/apply", byeA, map[string]any{"prune": true}); err == nil || !strings.Contains(err.Error(), "HTTP 422") || !strings.Contains(err.Error(), `banks[bank-c].settles_through \"bank-a\" is not a bank in this file`) {
		t.Fatalf("removing a bank another one settles through: %v", err)
	}
	byeC := applied()
	byeC.Spec.Banks = byeC.Spec.Banks[:2]
	if _, err := post("/apply", byeC, nil); err == nil || !strings.Contains(err.Error(), "HTTP 409") || !strings.Contains(err.Error(), "prune_required") || !strings.Contains(err.Error(), "The plan removes bank-c. Nothing was changed. Run again with --prune to remove it.") {
		t.Fatalf("a removal without prune: %v", err)
	}
	if applied().Spec.Banks[2].ID != "bank-c" || generations()["Bank/bank-c"] != 2 {
		t.Fatal("a refused removal changed the store")
	}
	plan, err = post("/plan", byeC, map[string]any{"money": map[string]any{"money": "transfer", "to": "bank-a"}})
	if err != nil {
		t.Fatal(err)
	}
	im := plan["actions"].([]any)[0].(map[string]any)["impact"].(map[string]any)
	if im["transfers"] != float64(1) || im["net_minor"] != float64(10000) || im["money_minor"] != float64(10000) || im["customers"] != float64(1) || len(im["databases"].([]any)) != 1 ||
		!strings.Contains(fmt.Sprint(plan["text"]), "transfer ₦100.00 for 1 customer to Bank A (on_remove)") || !strings.Contains(fmt.Sprint(plan["text"]), "settle 1 open transfer (net ₦100.00)") {
		t.Fatalf("impact of removing bank-c: %v", plan)
	}

	total := func(p persona) int64 {
		var sum int64
		for _, v := range s.balances(p) {
			sum += v
		}
		return sum
	}
	home, held := s.persona("adaeze"), total(s.persona("adaeze"))
	if _, err := post("/apply", byeC, map[string]any{"prune": true}); err != nil {
		t.Fatal(err)
	}
	s.waitBlocked("bank-c", setup.ReasonMoneyDecisionNeeded, "Abuja Digital holds ₦100.00 for 1 customer. Run beerus apply --prune --money transfer --to <bank>.")
	moveTo := map[string]any{"money": "transfer", "to": "bank-a", "customers": map[string]string{"cus_nobody": "leave"}}
	if _, err := post("/apply", byeC, map[string]any{"prune": true, "money": moveTo}); err != nil {
		t.Fatal(err)
	}
	s.waitBlocked("bank-c", setup.ReasonMoneyDecisionNeeded, "cus_nobody is not a customer of Abuja Digital.")
	moveTo["customers"] = map[string]string{who.Customer: "transfer"}
	if _, err := post("/apply", byeC, map[string]any{"prune": true, "money": moveTo}); err != nil {
		t.Fatal(err)
	}
	s.waitBlocked("bank-c", setup.ReasonSettlementPending, "NIP sessions with this bank have not settled")
	s.ctl("POST", "/nip/settle-now", nil)
	s.waitGone("bank", "bank-c")
	if got := total(home); got != held+10000 {
		t.Fatalf("adaeze holds %d at bank-a after bank-c sent her money, want %d", got, held+10000)
	}
	if _, err := post("/apply", withC, nil); err == nil || !strings.Contains(err.Error(), "banks[bank-c].id bank-c belonged to a removed bank and stays reserved") {
		t.Fatalf("adding the removed bank back: %v", err)
	}

	withP := applied()
	withP.Spec.Partners = append(withP.Spec.Partners, setup.Partner{ID: "ledgerly", Name: "Ledgerly", RedirectURIs: []string{"https://ledgerly.localhost/cb"}})
	out, err := post("/apply", withP, nil)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := s.cfg.Path("partners", "ledgerly", "api-key")
	if files := fmt.Sprint(out["api_key_files"]); files != "["+keyFile+"]" {
		t.Fatalf("where the key is: %v", out["api_key_files"])
	}
	key, err := config.ReadSecret(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	pr := settled("partner", "ledgerly")["partner"].(map[string]any)
	if pr["api_key_sha256"] != config.HashKey(key) || pr["cert_cn"] != "ledgerly" || strings.Contains(fmt.Sprint(pr), key) {
		t.Fatalf("partner status: %v", pr)
	}
	if code := rawGet(t, s, partnerClient(t, s, "ledgerly"), key, "/v1/webhook_keys"); code != 200 {
		t.Fatalf("the partner made from the spec got %d", code)
	}
	withP.Spec.Partners[len(withP.Spec.Partners)-1].WebhookURL = "https://ledgerly.localhost/hook"
	if _, err := post("/apply", withP, nil); err != nil {
		t.Fatal(err)
	}
	if got := s.waitResource("partner", "ledgerly", 60*time.Second, func(r map[string]any) bool { return r["observed_generation"] == float64(2) }); got["phase"] != setup.PhaseActive {
		t.Fatalf("partner update: %v", got)
	}
	if got := s.loadConfig().Partner("ledgerly"); got == nil || got.WebhookURL != "https://ledgerly.localhost/hook" {
		t.Fatalf("saved partner: %+v", got)
	}
	noP := applied()
	noP.Spec.Partners = slices.DeleteFunc(noP.Spec.Partners, func(p setup.Partner) bool { return p.ID == "ledgerly" })
	if _, err := post("/apply", noP, map[string]any{"prune": true}); err != nil {
		t.Fatal(err)
	}
	s.waitGone("partner", "ledgerly")
	if _, err := os.Stat(s.cfg.Path("partners", "ledgerly")); !os.IsNotExist(err) {
		t.Fatalf("the removed partner's files are still there: %v", err)
	}

	seen, cursor := []string{}, ""
	for {
		page := s.ctl("GET", "/resources?limit=2&cursor="+cursor, nil)
		for _, d := range page["data"].([]any) {
			m := d.(map[string]any)
			seen = append(seen, fmt.Sprint(m["kind"], "/", m["id"]))
		}
		next, _ := page["next_cursor"].(string)
		if next == "" {
			break
		}
		cursor = next
	}
	if want := []string{"Bank/bank-a", "Bank/bank-b", "Partner/balance", "System/cbn", "System/nibss"}; !slices.Equal(seen, want) {
		t.Fatalf("resources %v, want %v", seen, want)
	}
	if _, err := s.ctlTry("GET", "/resources/bank/nope", nil); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("an unknown resource: %v", err)
	}
	if _, err := s.ctlTry("GET", "/resources?limit=0", nil); err == nil || !strings.Contains(err.Error(), "HTTP 422") {
		t.Fatalf("a bad limit: %v", err)
	}
}
