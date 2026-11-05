package contracttest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/connect"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

func (s *Stack) waitBank(id string) map[string]any {
	s.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var last map[string]any
	for time.Now().Before(deadline) {
		for _, d := range s.ctl("GET", "/banks", nil)["data"].([]any) {
			st := d.(map[string]any)
			if st["id"] == id {
				last = st
				if st["phase"] != connect.PhaseProvisioning {
					return st
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	s.t.Fatalf("bank %s still provisioning: %v", id, last)
	return nil
}

func TestCreateBank(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newBankStack(t)
	snap := s.ctl("POST", "/snapshots", map[string]any{"name": "before"})
	out := s.ctl("POST", "/banks", map[string]any{"name": "Abuja Digital", "type": "digital"})
	if out["id"] != "abuja-digital" || out["phase"] != connect.PhaseProvisioning {
		t.Fatalf("create: %v", out)
	}
	st := s.waitBank("abuja-digital")
	if st["phase"] != connect.PhaseActive {
		t.Fatalf("phase: %v", st)
	}
	for _, step := range st["steps"].([]any) {
		if step.(map[string]any)["done"] != true {
			t.Fatalf("step not done: %v", step)
		}
	}
	cfg, err := connect.LoadConfig(s.cfg.Dir)
	if err != nil {
		t.Fatal(err)
	}
	b := cfg.Bank("abuja-digital")
	if b == nil || b.Type != "digital" || b.NIPCode == "" || b.NIPCode == "999003" || b.NIPCode == "999004" || b.Database != config.DatabaseName(cfg.Dir, "abuja-digital") {
		t.Fatalf("saved bank: %+v", b)
	}
	claimed := map[string]bool{}
	for _, o := range s.owned(setup.KindBank, "abuja-digital") {
		claimed[o.Kind] = true
	}
	if !claimed["database"] || !claimed["port"] {
		t.Fatalf("owned: %+v", s.owned(setup.KindBank, "abuja-digital"))
	}
	resp, err := http.Get(b.URL() + "/login")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "Abuja Digital") {
		t.Fatalf("online banking: %d %.200s", resp.StatusCode, body)
	}
	ob := s.bankBrowser()
	ob.base = b.URL()
	p := s.persona("adaeze")
	ob.get("/login")
	ob.post("/login", url.Values{"username": {p.Username}, "password": {p.Password}})
	if !strings.Contains(ob.last, "has no accounts at Abuja Digital") {
		t.Fatalf("sign-in at a bank with no customers: %.300s", ob.last)
	}
	ls := s.must(s.call("POST", "/v1/link_sessions", map[string]any{"start_date": s.today().String(), "workspace_id": "w", "redirect_uri": s.redirect}), 201)
	br := s.browser()
	br.get(fmt.Sprint(ls.JSON["widget_url"]))
	if !strings.Contains(br.last, "Abuja Digital") {
		t.Fatal("the widget does not list Bank C")
	}
	for _, bad := range []map[string]any{
		{"name": "Abuja Digital"},
		{"name": "Bank C"},
		{"name": "bank d microfinance"},
		{"name": "Bank E", "type": "credit-union"},
		{"name": "Bank F", "nip_code": "999003"},
		{"name": "Bank G", "nip_code": b.NIPCode},
		{"name": "Bank H", "history_days": 5000},
		{"name": "Bank I", "settles_through": "nowhere"},
	} {
		if _, err := s.ctlTry("POST", "/banks", bad); err == nil || !strings.Contains(err.Error(), "HTTP 422") {
			t.Errorf("%v: %v", bad, err)
		}
	}
	s.ctl("POST", "/snapshots/"+fmt.Sprint(snap["id"])+"/restore", nil)
	if st := s.waitBank("abuja-digital"); st["phase"] != connect.PhaseActive {
		t.Fatalf("after restore: %v", st)
	}
	resp, err = http.Get(b.URL() + "/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("online banking after restore: %d", resp.StatusCode)
	}
	s.bankSignIn("adaeze")
}

func TestSuspendBank(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newBankStack(t)
	conn := s.connect("chidi", "bank-b", s.today().AddDays(-10))
	keys := map[string]string{}
	for _, p := range s.ctl("GET", "/personas", nil)["data"].([]any) {
		for _, a := range p.(map[string]any)["accounts"].([]any) {
			m := a.(map[string]any)
			keys[fmt.Sprint(p.(map[string]any)["id"], "@", m["institution"])] = fmt.Sprint(m["key"])
		}
	}
	st := s.ctl("POST", "/banks/bank-b/suspend", nil)
	if st["phase"] != connect.PhaseSuspended {
		t.Fatalf("suspend: %v", st)
	}
	if _, err := s.ctlTry("POST", "/transfers", map[string]any{"from": keys["chidi@bank-b"], "to": keys["tunde@bank-a"], "amount": "1000"}); err == nil || !strings.Contains(err.Error(), "bank_suspended") {
		t.Fatalf("transfer out of a suspended bank: %v", err)
	}
	if _, err := s.ctlTry("POST", "/transfers", map[string]any{"from": keys["tunde@bank-a"], "to": keys["chidi@bank-b"], "amount": "1000"}); err == nil || !strings.Contains(err.Error(), "nothing was debited") {
		t.Fatalf("transfer into a suspended bank: %v", err)
	}
	s.syncNow()
	c := s.must(s.call("GET", "/v1/connections/"+conn.ConnectionID, nil), 200)
	if c.JSON["status"] != "institution_down" {
		t.Fatalf("status while suspended: %v", c.JSON["status"])
	}
	resp, err := http.Get(s.cfg.Bank("bank-b").URL() + "/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("online banking while suspended: %d", resp.StatusCode)
	}
	cfg, err := connect.LoadConfig(s.cfg.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Bank("bank-b").Suspended() {
		t.Fatal("suspension not saved")
	}
	s.ctl("POST", "/banks/bank-b/resume", nil)
	s.ctl("POST", "/transfers", map[string]any{"from": keys["tunde@bank-a"], "to": keys["chidi@bank-b"], "amount": "1000"})
	s.syncNow()
	c = s.must(s.call("GET", "/v1/connections/"+conn.ConnectionID, nil), 200)
	if c.JSON["status"] != "active" {
		t.Fatalf("status after resume: %v", c.JSON["status"])
	}
	if _, err := s.ctlTry("POST", "/banks/nowhere/suspend", nil); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("unknown bank: %v", err)
	}
}

func (s *Stack) waitPhase(id, phase string, within time.Duration) map[string]any {
	s.t.Helper()
	deadline := time.Now().Add(within)
	var last map[string]any
	for time.Now().Before(deadline) {
		for _, d := range s.ctl("GET", "/banks", nil)["data"].([]any) {
			st := d.(map[string]any)
			if st["id"] == id {
				last = st
				if st["phase"] == phase {
					return st
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	s.t.Fatalf("bank %s never reached %s: %v", id, phase, last)
	return nil
}

func TestBankRepairsDrift(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newBankStack(t)
	conn := s.connect("chidi", "bank-b", s.today().AddDays(-10))
	acct := fmt.Sprint(conn.Accounts[0]["id"])
	asOf := s.today().AddDays(-1).String()
	before := s.must(s.call("GET", "/v1/accounts/"+acct+"/balance?as_of="+asOf, nil), 200).JSON["ledger_balance"]
	pc, err := pgx.ParseConfig(s.cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	pc.Database = "postgres"
	db, err := pgx.ConnectConfig(context.Background(), pc)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	dropped := time.Now()
	if _, err := db.Exec(context.Background(), "DROP DATABASE "+s.cfg.Bank("bank-b").Database+" WITH (FORCE)"); err != nil {
		t.Fatal(err)
	}
	s.advance("24h")
	deadline := time.Now().Add(30 * time.Second)
	for {
		st := s.waitPhase("bank-b", connect.PhaseActive, 30*time.Second)
		rebuilt, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(st["steps"].([]any)[2].(map[string]any)["at"]))
		if rebuilt.After(dropped) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("bank-b was not rebuilt: %v", st)
		}
		time.Sleep(200 * time.Millisecond)
	}
	s.syncNow()
	if r := s.call("GET", "/v1/accounts/"+acct+"/balance?as_of="+asOf, nil); r.Status != 409 || r.Code() != "reauth_required" {
		t.Fatalf("consents live in the bank database, so a rebuilt bank should ask for consent again: %d %s", r.Status, r.Body)
	}
	s.connect("chidi", "bank-b", s.today().AddDays(-10))
	after := s.must(s.call("GET", "/v1/accounts/"+acct+"/balance?as_of="+asOf, nil), 200).JSON["ledger_balance"]
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatalf("balance changed across the repair: %v then %v", before, after)
	}
	b := s.bankBrowser()
	b.base = s.cfg.Bank("bank-b").URL()
	p := s.persona("chidi")
	b.get("/login")
	b.post("/login", url.Values{"username": {p.Username}, "password": {p.Password}})
	if !strings.Contains(b.last, "verification code") {
		t.Fatalf("sign-in after repair: %.300s", b.last)
	}
	checkSettled(t, s.app)
}

func TestOpenAccountAtNewBank(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newBankStack(t)
	s.ctl("POST", "/banks", map[string]any{"name": "Abuja Digital", "type": "digital"})
	s.waitPhase("abuja-digital", connect.PhaseActive, 60*time.Second)
	acc := s.ctl("POST", "/banks/abuja-digital/accounts", map[string]any{"persona": "adaeze", "type": "current"})
	nuban := fmt.Sprint(acc["nuban"])
	if acc["owner"] != "adaeze" || len(nuban) != 10 {
		t.Fatalf("open account: %v", acc)
	}
	if _, err := s.ctlTry("POST", "/banks/abuja-digital/accounts", map[string]any{"persona": "adaeze", "type": "current"}); err == nil || !strings.Contains(err.Error(), "HTTP 422") {
		t.Fatalf("second current account: %v", err)
	}
	from := ""
	for _, p := range s.ctl("GET", "/personas", nil)["data"].([]any) {
		for _, a := range p.(map[string]any)["accounts"].([]any) {
			m := a.(map[string]any)
			if p.(map[string]any)["id"] == "adaeze" && m["institution"] == "bank-a" && m["type"] == "current" {
				from = fmt.Sprint(m["key"])
			}
		}
	}
	s.ctl("POST", "/transfers", map[string]any{"from": from, "to": fmt.Sprint(acc["key"]), "amount": "25000"})
	b := s.bankBrowser()
	b.base = s.cfg.Bank("abuja-digital").URL()
	p := s.persona("adaeze")
	b.get("/login")
	b.post("/login", url.Values{"username": {p.Username}, "password": {p.Password}})
	m := regexp.MustCompile(`verification code is <span[^>]*>(\d{6})</span>`).FindStringSubmatch(b.last)
	if m == nil {
		t.Fatalf("sign-in at the new bank: %.300s", b.last)
	}
	b.post("/verify", url.Values{"otp": {m[1]}})
	if !strings.Contains(b.last, nuban) || !strings.Contains(b.last, "25,000.00") {
		t.Fatalf("online banking does not show the funded account: %.600s", b.last)
	}
	conn := s.connect("adaeze", "abuja-digital", s.today().AddDays(-3))
	if len(conn.Accounts) != 1 || conn.Accounts[0]["account_number"] != nuban {
		t.Fatalf("connected accounts: %v", conn.Accounts)
	}
}

func TestCloseImpact(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newBankStack(t)
	s.ctl("POST", "/banks", map[string]any{"name": "Abuja Digital", "type": "digital"})
	s.waitPhase("abuja-digital", connect.PhaseActive, 60*time.Second)
	empty := s.ctl("GET", "/banks/abuja-digital/close", nil)
	if len(empty["customers"].([]any)) != 0 || empty["deposits_minor"] != float64(0) {
		t.Fatalf("new bank impact: %v", empty)
	}
	acc := s.ctl("POST", "/banks/abuja-digital/accounts", map[string]any{"persona": "adaeze", "type": "current"})
	from := ""
	for _, p := range s.ctl("GET", "/personas", nil)["data"].([]any) {
		for _, a := range p.(map[string]any)["accounts"].([]any) {
			m := a.(map[string]any)
			if p.(map[string]any)["id"] == "adaeze" && m["institution"] == "bank-a" && m["type"] == "current" {
				from = fmt.Sprint(m["key"])
			}
		}
	}
	s.ctl("POST", "/transfers", map[string]any{"from": from, "to": fmt.Sprint(acc["key"]), "amount": "25000"})
	s.connect("adaeze", "abuja-digital", s.today().AddDays(-3))
	imp := s.ctl("GET", "/banks/abuja-digital/close", nil)
	if imp["insured_limit_minor"] != float64(500_000_000) || len(imp["blockers"].([]any)) != 0 {
		t.Fatalf("impact: %v", imp)
	}
	customers := imp["customers"].([]any)
	if len(customers) != 1 || customers[0].(map[string]any)["persona"] != "adaeze" || customers[0].(map[string]any)["deposits_minor"] != float64(2_500_000) {
		t.Fatalf("customers: %v", customers)
	}
	accounts := imp["accounts"].([]any)
	if len(accounts) != 1 || accounts[0].(map[string]any)["connected"] != true {
		t.Fatalf("accounts: %v", accounts)
	}
	if conns := imp["connections"].([]any); len(conns) != 1 || conns[0].(map[string]any)["partner"] != "balance" {
		t.Fatalf("connections: %v", conns)
	}
	if st := s.ctl("GET", "/banks", nil); !strings.Contains(fmt.Sprint(st["data"]), "abuja-digital") {
		t.Fatalf("banks: %v", st)
	}
	s.waitPhase("abuja-digital", connect.PhaseActive, time.Second)
	big := s.ctl("GET", "/banks/bank-a/close", nil)
	if big["insured_minor"].(float64)+big["uninsured_minor"].(float64) != big["deposits_minor"].(float64) || len(big["customers"].([]any)) == 0 {
		t.Fatalf("bank-a impact: %v", big)
	}
	if _, err := s.ctlTry("GET", "/banks/nope/close", nil); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("unknown bank: %v", err)
	}
}

func TestCloseBank(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newBankStack(t)
	s.ctl("POST", "/banks", map[string]any{"name": "Abuja Digital", "type": "digital"})
	s.waitPhase("abuja-digital", connect.PhaseActive, 60*time.Second)
	w := s.app.World()
	bankA := func(persona string) world.Account {
		for _, a := range w.AccountsAt(persona, "bank-a") {
			if a.Type == "current" {
				return a
			}
		}
		t.Fatalf("%s has no current account at bank-a", persona)
		return world.Account{}
	}
	fund := map[string]string{"adaeze": "25000", "tunde": "10000"}
	opened := map[string]string{}
	for p, amount := range fund {
		acc := s.ctl("POST", "/banks/abuja-digital/accounts", map[string]any{"persona": p, "type": "current"})
		opened[p] = fmt.Sprint(acc["key"])
		s.ctl("POST", "/transfers", map[string]any{"from": bankA(p).Key, "to": opened[p], "amount": amount})
	}
	conn := s.connect("adaeze", "abuja-digital", s.today().AddDays(-3))
	acct := fmt.Sprint(conn.Accounts[0]["id"])
	balance := func(key string) int64 {
		v, err := w.LedgerAt(key, s.app.Clock().Now())
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := balance(bankA("adaeze").Key)
	for _, bad := range []map[string]any{{"money": "transfer"}, {"money": "transfer", "to": "abuja-digital"}, {"money": "keep", "to": "bank-a"}, {"money": "transfer", "to": "bank-a", "customers": map[string]string{"chidi": "leave"}}} {
		if _, err := s.ctlTry("POST", "/banks/abuja-digital/close", bad); err == nil || !strings.Contains(err.Error(), "HTTP 422") {
			t.Fatalf("close with %v: %v", bad, err)
		}
	}
	res := s.ctl("POST", "/banks/abuja-digital/close", map[string]any{"money": "transfer", "to": "bank-a", "customers": map[string]string{"tunde": "leave"}})
	if res["moved_minor"] != float64(2_500_000) || res["left_minor"] != float64(1_000_000) || len(res["revoked_connections"].([]any)) != 1 {
		t.Fatalf("close: %v", res)
	}
	s.waitPhase("abuja-digital", connect.PhaseClosed, time.Second)
	if got := balance(bankA("adaeze").Key); got != before+2_500_000 {
		t.Fatalf("adaeze at bank-a has %d, want %d", got, before+2_500_000)
	}
	s.advance("72h")
	if got := balance(opened["adaeze"]); got != 0 {
		t.Fatalf("closed account keeps %d", got)
	}
	if got := balance(opened["tunde"]); got != 1_000_000 {
		t.Fatalf("left account has %d", got)
	}
	if c, err := s.app.VerifyBank(); err != nil || !c.Ledger.OK() || len(c.Mismatches) > 0 {
		t.Fatalf("bank ledgers after the close: %v %v %v", err, c.Ledger, c.Mismatches)
	}
	if got := s.eventsOf("connection.status_changed", func(d map[string]any) bool {
		return d["connection_id"] == conn.ConnectionID && d["status"] == "revoked"
	}); len(got) != 1 {
		t.Fatalf("revoked webhooks: %v", got)
	}
	if got := s.eventsOf("account.removed", func(d map[string]any) bool { return d["account_id"] == acct }); len(got) != 1 {
		t.Fatalf("account.removed webhooks: %v", got)
	}
	s.must(s.call("GET", "/v1/accounts/"+acct+"/balance", nil), 409)
	if _, err := http.Get(s.cfg.Bank("abuja-digital").URL() + "/login"); err == nil {
		t.Fatal("online banking still answers")
	}
	for _, path := range []string{"/banks/abuja-digital/resume", "/banks/abuja-digital/suspend", "/banks/abuja-digital/close"} {
		if _, err := s.ctlTry("POST", path, map[string]any{"to": "bank-a"}); err == nil || !strings.Contains(err.Error(), "HTTP 422") {
			t.Fatalf("%s on a closed bank: %v", path, err)
		}
	}
	if _, err := s.ctlTry("POST", "/transfers", map[string]any{"from": bankA("tunde").Key, "to": opened["tunde"], "amount": "100"}); err == nil || !strings.Contains(err.Error(), "bank_closed") {
		t.Fatalf("transfer into a closed bank: %v", err)
	}
	if _, err := s.ctlTry("POST", "/banks/abuja-digital/accounts", map[string]any{"persona": "chidi"}); err == nil || !strings.Contains(err.Error(), "HTTP 422") {
		t.Fatalf("open account at a closed bank: %v", err)
	}
	s.waitPhase("abuja-digital", connect.PhaseClosed, time.Second)
	if err := s.app.Save(); err != nil {
		t.Fatal(err)
	}
	saved := s.loadConfig()
	if !saved.Bank("abuja-digital").Closed() {
		t.Fatal("the closed state was not saved")
	}
	again, err := connect.New(saved, connect.Options{WorldOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range again.BankStatuses() {
		if st.ID == "abuja-digital" && st.Phase != connect.PhaseClosed {
			t.Fatalf("after restart: %s", st.Phase)
		}
	}
	if v, _ := again.World().LedgerAt(bankA("adaeze").Key, s.app.Clock().Now()); v != balance(bankA("adaeze").Key) {
		t.Fatalf("money moved again after restart: %d vs %d", v, balance(bankA("adaeze").Key))
	}
	checkSettled(t, s.app)
}

func TestClosePaysInsuredDeposits(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newBankStack(t)
	s.ctl("POST", "/banks", map[string]any{"name": "Kano Microfinance", "type": "microfinance"})
	s.waitPhase("kano-microfinance", connect.PhaseActive, 60*time.Second)
	if imp := s.ctl("GET", "/banks/bank-a/close", nil); !strings.Contains(fmt.Sprint(imp["blockers"]), "Kano Microfinance settles through this bank") {
		t.Fatalf("bank-a blockers: %v", imp["blockers"])
	}
	w := s.app.World()
	now := func() time.Time { return s.app.Clock().Now() }
	var who world.Persona
	var from world.Account
	for _, p := range w.Personas() {
		for _, a := range w.AccountsAt(p.ID, "bank-a") {
			if bal, _ := w.LedgerAt(a.Key, now()); a.Type == "current" && bal > 260_000_000 && from.Key == "" {
				who, from = p, a
			}
		}
	}
	if from.Key == "" {
		t.Fatal("no persona has ₦2.6m in a bank-a current account")
	}
	held := map[string]string{"current": "1500000", "savings": "1000000"}
	for typ, amount := range held {
		acc := s.ctl("POST", "/banks/kano-microfinance/accounts", map[string]any{"persona": who.ID, "type": typ})
		s.ctl("POST", "/transfers", map[string]any{"from": from.Key, "to": fmt.Sprint(acc["key"]), "amount": amount})
	}
	total := func(bank string) int64 {
		var sum int64
		for _, a := range w.AccountsAt(who.ID, bank) {
			v, _ := w.LedgerAt(a.Key, now())
			sum += v
		}
		return sum
	}
	before := total("bank-a")
	res := s.ctl("POST", "/banks/kano-microfinance/close", map[string]any{"money": "payout", "to": "bank-a"})
	if res["paid_out_minor"] != float64(200_000_000) || res["claims_minor"] != float64(50_000_000) {
		t.Fatalf("close: %v", res)
	}
	if got := total("bank-a"); got != before+200_000_000 {
		t.Fatalf("%s at bank-a has %d, want %d", who.ID, got, before+200_000_000)
	}
	s.advance("72h")
	if got := total("kano-microfinance"); got != 50_000_000 {
		t.Fatalf("claims left at the closed bank: %d", got)
	}
	if imp := s.ctl("GET", "/banks/bank-a/close", nil); len(imp["blockers"].([]any)) != 0 {
		t.Fatalf("bank-a still blocked after the microfinance bank closed: %v", imp["blockers"])
	}
	checkSettled(t, s.app)
}

func TestRestoreCannotReopenAClosedBank(t *testing.T) {
	s := newStack(t)
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	snap := s.ctl("POST", "/snapshots", map[string]string{"name": "before"})
	s.ctl("POST", "/banks/bank-b/close", map[string]any{"money": "leave"})
	_, err := s.ctlTry("POST", "/snapshots/"+fmt.Sprint(snap["id"])+"/restore", nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 409") || !strings.Contains(err.Error(), "cannot be undone") {
		t.Fatalf("restore before the close: %v", err)
	}
	s.waitPhase("bank-b", connect.PhaseClosed, time.Second)
	if !s.app.World().Closed("bank-b", s.app.Clock().Now()) {
		t.Fatal("bank-b reopened in the world")
	}
	ids := func(state string) []string {
		out := []string{}
		for _, d := range s.ctl("GET", "/banks?state="+state, nil)["data"].([]any) {
			out = append(out, fmt.Sprint(d.(map[string]any)["id"]))
		}
		return out
	}
	if got := fmt.Sprint(ids("closed")); got != "[bank-b]" {
		t.Fatalf("closed banks: %s", got)
	}
	if got := fmt.Sprint(ids("active")); got != "[bank-a]" {
		t.Fatalf("active banks: %s", got)
	}
	if got := len(s.ctl("GET", "/banks", nil)["data"].([]any)); got != 2 {
		t.Fatalf("all banks: %d", got)
	}
}
