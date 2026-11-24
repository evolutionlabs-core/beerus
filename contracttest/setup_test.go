package contracttest

import (
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/aggregator"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"github.com/evolutionlabs-core/beerus/internal/core"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func TestSetupChanges(t *testing.T) {
	s := ownStack(t, aggregator.Options{})
	s.needsNodes()
	c := newCLIAt(t, s.cfg.Dir)
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
		return s.waitResource(kind, id, func(r map[string]any) bool {
			return r != nil && r["phase"] == config.PhaseActive && r["observed_generation"] == r["generation"]
		})
	}

	if _, err := post("/apply", applied(), map[string]any{"prune": true, "settle_now": true}); err == nil || !strings.Contains(err.Error(), "HTTP 422") || !strings.Contains(err.Error(), "settle_now needs a bank that is being closed or removed. Nothing was changed.") {
		t.Fatalf("settle_now: %v", err)
	}
	if _, err := s.ctlTry("POST", "/banks", map[string]any{"name": strings.Repeat("x", 41)}); err == nil || !strings.Contains(err.Error(), "has 41 characters. Use 1 to 40") {
		t.Fatalf("a long bank name: %v", err)
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

	renamedSpec := applied()
	renamedSpec.Spec.Banks[0].Name = "Abuja Digital"
	renamedRaw, err := setup.Export(renamedSpec)
	if err != nil {
		t.Fatal(err)
	}
	renamedFile := filepath.Join(t.TempDir(), "renamed.yaml")
	if err := os.WriteFile(renamedFile, renamedRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _, code := c.run("diff", "-f", renamedFile); code != 2 || !strings.Contains(got, "bank-a") || !strings.Contains(got, "Abuja Digital") || strings.Contains(got, "No changes.") {
		t.Errorf("diff of a rename: exit %d, %q", code, got)
	}
	if got := c.ok("diff"); got != "No changes.\n" {
		t.Errorf("diff of the applied file: %q", got)
	}
	if got := c.ok("apply"); got != "No changes.\n" {
		t.Errorf("a repeat apply: %q", got)
	}
	holiday := applied()
	holiday.Spec.Holidays.Add = []string{time.Now().AddDate(0, 0, -30).Format("2006-01-02")}
	holidayRaw, err := setup.Export(holiday)
	if err != nil {
		t.Fatal(err)
	}
	holidayFile := filepath.Join(t.TempDir(), "holiday.yaml")
	if err := os.WriteFile(holidayFile, holidayRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := c.fails("diff", "-f", holidayFile); !strings.Contains(got, "holidays") || !strings.Contains(got, "is in the past and cannot change") {
		t.Errorf("a holiday in the past: %q", got)
	}

	locked := applied()
	locked.Spec.Banks[0].NUBANCode = "777"
	if _, err := post("/apply", locked, nil); err == nil || !strings.Contains(err.Error(), "HTTP 422") || !strings.Contains(err.Error(), `"code":"invalid_spec"`) || !regexp.MustCompile(`setup\.yaml:\d+:\d+: banks\[bank-a\]\.nuban_code cannot change`).MatchString(err.Error()) {
		t.Fatalf("a locked field: %v", err)
	}

	withC := applied()
	withC.Spec.Banks = append(withC.Spec.Banks, setup.Bank{ID: "bank-c", Name: "Bank C", Type: "digital", SettlesThrough: "bank-a"})
	plan, err := post("/plan", withC, nil)
	if err != nil || !strings.Contains(fmt.Sprint(plan["text"]), "+ bank bank-c") || !strings.Contains(fmt.Sprint(plan["text"]), "NIP 000003 (allocated), NUBAN 003 (allocated)") {
		t.Fatalf("plan to add a bank: %v %v", plan, err)
	}
	if got := generations(); !maps.Equal(before, got) {
		t.Fatalf("a plan moved generations: %v", got)
	}
	if _, err := post("/apply", withC, nil); err != nil {
		t.Fatal(err)
	}
	r := settled("bank", "bank-c")
	if pinned := applied().Spec.Banks[2]; pinned.NIPCode != "000003" || pinned.NUBANCode != "003" || r["generation"] != float64(1) {
		t.Fatalf("bank-c after the apply: %+v %v", pinned, r)
	}
	who := s.join("adaeze", "bank-c")
	nuban := s.openAccount(who, "current")
	enquiry := func() map[string]any {
		return s.ctl("GET", "/nip/name-enquiry?bank_code=000003&account_number="+nuban, nil)
	}
	if ne := enquiry(); ne["response_code"] != "00" || ne["account_name"] != who.Name || ne["bank_name"] != "Bank C" {
		t.Fatalf("name enquiry at the new bank: %v", ne)
	}
	s.pay("bank-a", s.persona("adaeze").Accounts[0], "bank-c", nuban, "100")

	site := s.cfg.Bank("bank-c").URL()
	signedIn := s.browser()
	signedIn.get(site + "/login")
	signedIn.postTo(site, "/login", url.Values{"username": {who.Username}, "password": {who.Password}})
	if otp := reOTP.FindStringSubmatch(signedIn.last); otp != nil {
		signedIn.postTo(site, "/verify", url.Values{"otp": {otp[1]}})
	}
	if signedIn.get(site+"/accounts") != http.StatusOK || signedIn.cur.Path != "/accounts" || !strings.Contains(signedIn.last, nuban) {
		t.Fatalf("sign in at the new bank: %v %.300s", signedIn.cur, signedIn.last)
	}

	renamed := applied()
	renamed.Spec.Banks[2].Name = "Abuja Digital"
	if _, err := post("/apply", renamed, nil); err != nil {
		t.Fatal(err)
	}
	if ne := enquiry(); ne["response_code"] != "00" {
		t.Fatalf("name enquiry during the rename: %v", ne)
	}
	r = settled("bank", "bank-c")
	if r["generation"] != float64(2) {
		t.Fatalf("a rename should bump the generation once: %v", r)
	}
	if ne := enquiry(); ne["bank_name"] != "Abuja Digital" || ne["response_code"] != "00" {
		t.Fatalf("name enquiry after the rename: %v", ne)
	}
	if signedIn.get(site+"/accounts") != http.StatusOK || signedIn.cur.Path != "/accounts" || !strings.Contains(signedIn.last, nuban) {
		t.Fatalf("the rename signed the customer out: %v %.300s", signedIn.cur, signedIn.last)
	}
	var page []byte
	waitFor(t, waitLimit, "the bank site to show the new name", func() bool {
		resp, err := s.raw.Get(site + "/login")
		if err != nil {
			return false
		}
		page, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		return strings.Contains(string(page), "Abuja Digital")
	})
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
	byeCRaw, err := setup.Export(byeC)
	if err != nil {
		t.Fatal(err)
	}
	byeCFile := filepath.Join(t.TempDir(), "bye-c.yaml")
	if err := os.WriteFile(byeCFile, byeCRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := c.fails("apply", "-f", byeCFile); !strings.Contains(got, "--prune") {
		t.Errorf("apply of a removal without --prune: %q", got)
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
	if _, err := post("/apply", byeC, map[string]any{"prune": true, "settle_now": true}); err != nil {
		t.Fatal(err)
	}
	s.waitBlocked("bank-c", setup.ReasonMoneyDecisionNeeded, "Abuja Digital holds ₦100.00 for 1 customer. Choose --money transfer --to <bank>, --money payout or --money leave. Nothing moves until you do.")
	moveTo := map[string]any{"money": "transfer", "to": "bank-a", "customers": map[string]string{"cus_nobody": "leave"}}
	if _, err := post("/apply", byeC, map[string]any{"prune": true, "money": moveTo}); err != nil {
		t.Fatal(err)
	}
	s.waitBlocked("bank-c", setup.ReasonMoneyDecisionNeeded, "cus_nobody is not a customer of Abuja Digital.")
	moveTo["customers"] = map[string]string{who.Customer: "transfer"}
	if _, err := post("/apply", byeC, map[string]any{"prune": true, "money": moveTo}); err != nil {
		t.Fatal(err)
	}
	s.waitGone("bank", "bank-c")
	checkSettled(t, s.app)
	if got := total(home); got != held+10000 {
		t.Fatalf("adaeze holds %d at bank-a after bank-c sent her money, want %d", got, held+10000)
	}
	if _, err := post("/apply", withC, nil); err == nil || !strings.Contains(err.Error(), "banks[bank-c].id bank-c belonged to a removed bank. Choose another id.") {
		t.Fatalf("adding the removed bank back: %v", err)
	}

	withP := applied()
	withP.Spec.Partners = append(withP.Spec.Partners, setup.Partner{ID: "ledgerly", Name: "Ledgerly", RedirectURIs: []string{"https://ledgerly.localhost/cb"}})
	out, err := post("/apply", withP, nil)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := s.cfg.Path("partners", "ledgerly", config.PartnerSecretFile)
	if files := fmt.Sprint(out["secret_files"]); files != "["+keyFile+"]" {
		t.Fatalf("where the secret is: %v", out["secret_files"])
	}
	key, err := config.ReadSecret(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	pr := settled("partner", "ledgerly")["partner"].(map[string]any)
	if pr["secret_sha256"] != config.HashKey(key) || strings.Contains(fmt.Sprint(pr), key) {
		t.Fatalf("partner status: %v", pr)
	}
	if code := rawGet(s, "ledgerly", key, "", "/v1/webhook_keys"); code != 200 {
		t.Fatalf("the partner made from the spec got %d", code)
	}
	withP.Spec.Partners[len(withP.Spec.Partners)-1].WebhookURL = "https://ledgerly.localhost/hook"
	if _, err := post("/apply", withP, nil); err != nil {
		t.Fatal(err)
	}
	if got := s.waitResource("partner", "ledgerly", func(r map[string]any) bool { return r["observed_generation"] == float64(2) }); got["phase"] != config.PhaseActive {
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
	if want := []string{"Bank/bank-a", "Bank/bank-b", "Partner/balance", "System/cbn", "System/connect", "System/nibss"}; !slices.Equal(seen, want) {
		t.Fatalf("resources %v, want %v", seen, want)
	}
	if _, err := s.ctlTry("GET", "/resources/bank/nope", nil); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("an unknown resource: %v", err)
	}
	if _, err := s.ctlTry("GET", "/resources?limit=0", nil); err == nil || !strings.Contains(err.Error(), "HTTP 422") {
		t.Fatalf("a bad limit: %v", err)
	}

	revoked := func() int {
		n := 0
		for key := range s.consentTrail("bank-a") {
			if strings.Contains(key, " Revoked/") {
				n++
			}
		}
		return n
	}
	s.connect("adaeze", "bank-a")
	revokedBefore := revoked()
	noBalance := applied()
	noBalance.Spec.Partners = slices.DeleteFunc(noBalance.Spec.Partners, func(p setup.Partner) bool { return p.ID == "balance" })
	if _, err := post("/apply", noBalance, map[string]any{"prune": true}); err != nil {
		t.Fatal(err)
	}
	s.waitGone("partner", "balance")
	waitFor(t, waitLimit, "the revoke in the NIBSS consent ledger", func() bool { return revoked() > revokedBefore })
	if got := revoked(); got != revokedBefore+1 {
		t.Fatalf("the removed partner's connection was revoked at the bank %d times, want 1", got-revokedBefore)
	}
	if rows := s.ctl("GET", "/connections", nil)["data"].([]any); len(rows) != 0 {
		t.Fatalf("the removed partner's connections are still stored: %v", rows)
	}

	s.t.Log("purge rules")
	{
		s.ctl("POST", "/banks", map[string]any{"name": "Acme Bank", "type": "digital"})
		s.waitPhase("acme-bank", core.PhaseActive)
		oldBank, err := s.app.Resource(setup.KindBank, "acme-bank")
		if err != nil {
			t.Fatal(err)
		}
		oldCode := oldBank.Allocated.NIPCode
		home := s.persona("adaeze")
		target := s.openAccount(s.join("adaeze", "acme-bank"), "current")
		s.payListed("bank-a", home.Accounts[0], "acme-bank", target, "1000")
		s.ctl("POST", "/nip/settle-now", nil)
		s.ctl("POST", "/faults", map[string]any{"kind": "timeout", "bank": "acme-bank", "outcome": "credited", "count": 5})
		s.ctl("POST", "/banks/acme-bank/close", map[string]any{"money": "leave"})
		s.waitPhase("acme-bank", core.PhaseClosed)
		oldBalance := cbnBalance(t, s.cfg, "acme-bank")
		s.ctl("POST", "/banks/acme-bank/purge", nil)
		s.waitGone("bank", "acme-bank")
		if tombs := storedStatus(t, s.cfg.Dir).Tombstones; !slices.ContainsFunc(tombs, func(tb nip.Institution) bool {
			return tb.ID == "acme-bank" && tb.NIPCode == oldCode && tb.Status == "removed"
		}) {
			t.Fatalf("tombstones after the purge: %+v", tombs)
		}
		if _, err := s.ctlTry("POST", "/banks", map[string]any{"name": "Acme Two", "id": "acme-bank"}); err == nil || !strings.Contains(err.Error(), "HTTP 422") || !strings.Contains(err.Error(), "banks[acme-bank].id acme-bank belonged to a removed bank. Choose another id.") {
			t.Fatalf("create with a purged id: %v", err)
		}
		if _, err := s.ctlTry("POST", "/banks", map[string]any{"name": "Acme Three", "nip_code": "123456"}); err == nil || !strings.Contains(err.Error(), "HTTP 422") || !strings.Contains(err.Error(), "nip_code and nuban_code are assigned by Beerus; leave them out") {
			t.Fatalf("create with a chosen code: %v", err)
		}
		s.ctl("POST", "/banks", map[string]any{"name": "Acme Bank", "type": "digital"})
		s.waitPhase("acme-bank-2", core.PhaseActive)
		s.ctl("POST", "/banks", map[string]any{"name": "Bank-B", "type": "digital"})
		s.waitPhase("bank-b-2", core.PhaseActive)
		fresh, err := s.app.Resource(setup.KindBank, "acme-bank-2")
		if err != nil {
			t.Fatal(err)
		}
		if fresh.Allocated.NIPCode == oldCode {
			t.Fatalf("the recreated bank reused the code %s", oldCode)
		}
		if got := cbnBalance(t, s.cfg, "acme-bank-2"); got != control.GenesisSettlement {
			t.Fatalf("the recreated bank has %d at the CBN, want %d", got, control.GenesisSettlement)
		}
		if got := cbnBalance(t, s.cfg, "acme-bank"); got != oldBalance {
			t.Fatalf("the purged bank's CBN account holds %d, want %d", got, oldBalance)
		}
		if faults := s.ctl("GET", "/faults", nil); strings.Contains(fmt.Sprint(faults), "acme-bank") {
			t.Fatalf("a fault of the purged bank survives: %v", faults)
		}
		for _, id := range []string{"acme-bank-2", "bank-b-2", "bank-b", "bank-a"} {
			s.ctl("POST", "/banks/"+id+"/close", map[string]any{"money": "leave"})
			s.waitPhase(id, core.PhaseClosed)
			if imp := s.ctl("GET", "/banks/"+id+"/purge", nil); fmt.Sprint(imp["blockers"]) != "[]" {
				t.Fatalf("purge preview of %s: %v", id, imp)
			}
			s.ctl("POST", "/banks/"+id+"/purge", nil)
			s.waitGone("bank", id)
		}
		if left := s.ctl("GET", "/banks", nil)["data"].([]any); len(left) != 0 {
			t.Fatalf("banks after purging every bank: %v", left)
		}
		if err := s.app.Save(); err != nil {
			t.Fatal(err)
		}
		saved := s.loadConfig()
		if len(saved.Banks) != 0 {
			t.Fatal("the setup still lists a purged bank")
		}
		again, err := core.New(saved, core.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if left := again.BankStatuses(); len(left) != 0 {
			t.Fatalf("purged banks came back after a restart: %v", left)
		}
	}
}

func cbnBalance(t *testing.T, cfg *config.Config, account string) int64 {
	t.Helper()
	ctx := t.Context()
	st, err := store.Open(ctx, cfg.DatabaseURL, config.DatabaseName(cfg.Dir, setup.SystemCBN))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	bal, err := ledger.New(st.Pool, nil).Balance(ctx, "SETTLEMENT/"+account)
	if err != nil {
		t.Fatal(err)
	}
	return bal
}
