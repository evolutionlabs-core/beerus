package contracttest

import (
	"context"
	"fmt"
	"maps"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/connect"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func (s *Stack) applyWithout(id string, extra map[string]any) (map[string]any, error) {
	s.t.Helper()
	spec := storedStatus(s.t, s.cfg.Dir).Applied
	spec.Spec.Banks = slices.DeleteFunc(spec.Spec.Banks, func(b setup.Bank) bool { return b.ID == id })
	raw, err := setup.Export(spec)
	if err != nil {
		s.t.Fatal(err)
	}
	body := map[string]any{"file": "setup.yaml", "spec": string(raw)}
	maps.Copy(body, extra)
	return s.ctlTry("POST", "/apply", body)
}

func TestPurgeLeavesNothingBehind(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newBankStack(t)
	s.ctl("POST", "/banks", map[string]any{"name": "Abuja Digital", "type": "digital"})
	s.waitPhase("abuja-digital", connect.PhaseActive, 60*time.Second)
	bank := *s.cfg.Bank("abuja-digital")
	home := s.persona("adaeze")
	target := s.openAccount(s.join("adaeze", "abuja-digital"), "current")
	s.pay("bank-a", home.Accounts[0], "abuja-digital", target, "25000")
	conn := s.connect("adaeze", "abuja-digital", s.today().AddDays(-3))
	acct := fmt.Sprint(conn.Accounts[0]["id"])
	s.ctl("POST", "/faults", map[string]any{"kind": "timeout", "bank": "abuja-digital", "outcome": "credited", "count": 1})
	if sent, _ := s.payTry("bank-a", home.Accounts[0], "abuja-digital", target, "100", nil); sent["status"] == "success" {
		t.Fatalf("the timeout fault did not leave the transfer pending: %v", sent)
	}
	snap := fmt.Sprint(s.ctl("POST", "/snapshots", map[string]string{"name": "with abuja"})["id"])
	databases := func() []string {
		names, err := store.List(context.Background(), s.cfg.DatabaseURL, bank.Database)
		if err != nil {
			t.Fatal(err)
		}
		return names
	}
	if len(databases()) != 2 {
		t.Fatalf("before the purge: %v", databases())
	}
	imp := s.ctl("GET", "/banks/abuja-digital/purge", nil)
	if !strings.Contains(fmt.Sprint(imp["blockers"]), "to settle 2 transfers (net ₦25,100.00)") || fmt.Sprint(imp["snapshots"]) != "["+snap+"]" || imp["database"] != bank.Database || imp["connections"] != float64(1) {
		t.Fatalf("purge preview: %v", imp)
	}
	atBankA := s.balances(home)[home.Accounts[0]]
	if res := s.ctl("POST", "/banks/abuja-digital/purge", nil); res["phase"] != setup.PhaseTerminating {
		t.Fatalf("purge was not admitted as terminating: %v", res)
	}
	s.waitBlocked("abuja-digital", setup.ReasonSettlementPending, "to settle 2 transfers (net ₦25,100.00). Wait for it, or run beerus nip settle-now.")
	if got := databases(); len(got) != 2 {
		t.Fatalf("the purge dropped databases while transfers were unsettled: %v", got)
	}
	if ne := s.ctl("GET", "/nip/name-enquiry?bank_code="+bank.NIPCode+"&account_number="+target, nil); ne["response_code"] != "91" {
		t.Fatalf("name enquiry at a terminating bank: %v", ne)
	}
	if _, err := s.payTry("bank-a", home.Accounts[0], "abuja-digital", target, "100", nil); err == nil || !strings.Contains(err.Error(), "nothing was debited") {
		t.Fatalf("transfer into a terminating bank: %v", err)
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
	if _, err := s.ctlTry("POST", "/snapshots/"+snap+"/restore", nil); err == nil || !strings.Contains(err.Error(), "HTTP 409") || !strings.Contains(err.Error(), "Abuja Digital is terminating, so a snapshot cannot be restored until that finishes") {
		t.Fatalf("restore while a bank is terminating: %v", err)
	}
	if _, err := s.ctlTry("POST", "/banks", map[string]any{"name": "Abuja Digital", "id": "abuja-digital"}); err == nil || !strings.Contains(err.Error(), "HTTP 409") {
		t.Fatalf("re-adding a bank that is being removed: %v", err)
	}
	s.advance("24h")
	s.advance("24h")
	s.waitBlocked("abuja-digital", setup.ReasonMoneyDecisionNeeded, "Abuja Digital holds ₦25,100.00 for 1 customer. Choose --money transfer --to <bank>, --money payout or --money leave. Nothing moves until you do.")
	if got := databases(); len(got) != 2 {
		t.Fatalf("the purge dropped databases before the money was decided: %v", got)
	}
	if _, err := s.applyWithout("abuja-digital", map[string]any{"prune": true, "money": map[string]any{"money": "transfer", "to": "bank-a"}}); err != nil {
		t.Fatal(err)
	}
	s.waitBlocked("abuja-digital", setup.ReasonSettlementPending, "to settle 1 transfer (net ₦25,100.00)")
	if got := s.balances(home)[home.Accounts[0]]; got != atBankA+2_510_000 {
		t.Fatalf("adaeze at bank-a holds %d, want %d", got, atBankA+2_510_000)
	}
	checkSettled(t, s.app)
	s.ctl("POST", "/nip/settle-now", nil)
	s.waitGone("bank", "abuja-digital")
	kept := s.ctl("GET", "/closures/abuja-digital", nil)
	if lines := kept["lines"].([]any); len(lines) != 1 || lines[0].(map[string]any)["outcome"] != "transferred" || lines[0].(map[string]any)["amount_minor"] != float64(2_510_000) || lines[0].(map[string]any)["to_bank"] != "bank-a" || lines[0].(map[string]any)["to_nuban"] != home.Accounts[0] {
		t.Fatalf("closure kept after the purge: %v", kept)
	}
	if got := databases(); len(got) != 0 {
		t.Fatalf("databases left behind: %v", got)
	}
	if _, err := os.Stat(s.cfg.Path("snapshots", snap+".json")); !os.IsNotExist(err) {
		t.Fatalf("snapshot file left behind: %v", err)
	}
	c := s.loadConfig()
	if c.Bank("abuja-digital") != nil {
		t.Fatal("the setup still lists the bank")
	}
	if o := s.owned(setup.KindBank, "abuja-digital"); o != nil {
		t.Fatalf("the status store still claims %v", o)
	}
	if l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(bank.Port))); err != nil {
		t.Fatalf("port %d is still taken: %v", bank.Port, err)
	} else {
		l.Close()
	}
	for _, d := range s.ctl("GET", "/banks", nil)["data"].([]any) {
		if d.(map[string]any)["id"] == "abuja-digital" {
			t.Fatal("GET /banks still lists the bank")
		}
	}
	if _, err := s.ctlTry("GET", "/banks/abuja-digital/purge", nil); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("preview after the purge: %v", err)
	}
	if r := s.call("GET", "/v1/accounts/"+acct+"/balance", nil); r.Status != 404 && r.Status != 409 {
		t.Fatalf("partner balance after the purge: %d", r.Status)
	}
	for _, req := range []map[string]any{{"name": "Abuja Digital"}, {"name": "Another", "id": "abuja-digital"}, {"name": "Another", "nip_code": bank.NIPCode}, {"name": "Another", "nuban_code": bank.NUBANCode}} {
		if _, err := s.ctlTry("POST", "/banks", req); err == nil || !strings.Contains(err.Error(), "HTTP 422") {
			t.Fatalf("create %v after the purge: %v", req, err)
		}
	}
	rep, err := connect.Doctor(context.Background(), c, false)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Clean() || len(rep.Missing) != 0 {
		t.Fatalf("doctor: %+v", rep)
	}
	stray, err := store.Open(context.Background(), c.DatabaseURL, config.DatabaseName(c.Dir, "stray"))
	if err != nil {
		t.Fatal(err)
	}
	stray.Close()
	if rep, err := connect.Doctor(context.Background(), c, false); err != nil || fmt.Sprint(rep.Orphans) != "["+config.DatabaseName(c.Dir, "stray")+"]" {
		t.Fatalf("doctor with a stray database: %+v %v", rep, err)
	}
	if _, err := connect.Doctor(context.Background(), c, true); err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("doctor --fix while running: %v", err)
	}
	if err := os.Remove(c.Path("running")); err != nil {
		t.Fatal(err)
	}
	if rep, err := connect.Doctor(context.Background(), c, true); err != nil || !rep.Fixed {
		t.Fatalf("doctor --fix: %+v %v", rep, err)
	}
	if rep, err := connect.Doctor(context.Background(), c, false); err != nil || !rep.Clean() {
		t.Fatalf("doctor after --fix: %+v %v", rep, err)
	}
	if ck, err := s.app.VerifyBank(); err != nil || !ck.OK() {
		t.Fatalf("bank ledgers after the purge: %v %v", err, ck)
	}
	s.advance("72h")
	checkSettled(t, s.app)
	before := s.balances(home)
	if err := s.app.Save(); err != nil {
		t.Fatal(err)
	}
	again, err := connect.New(c, connect.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range again.BankStatuses() {
		if st.ID == "abuja-digital" {
			t.Fatal("the bank came back after a restart")
		}
		if st.Phase != connect.PhaseActive {
			t.Fatalf("%s is %s after a restart", st.ID, st.Phase)
		}
	}
	if after := s.balances(home); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("balances moved after a restart: %v, before %v", after, before)
	}
}

func TestPurgeRules(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newStubStack(t)
	s.ctl("POST", "/banks/bank-b/close", map[string]any{"money": "leave"})
	s.waitPhase("bank-b", connect.PhaseClosed, 60*time.Second)
	snap := fmt.Sprint(s.ctl("POST", "/snapshots", map[string]string{"name": "closed"})["id"])
	s.ctl("POST", "/banks/bank-b/purge", nil)
	s.waitGone("bank", "bank-b")
	if _, err := os.Stat(s.cfg.Path("snapshots", snap+".json")); !os.IsNotExist(err) {
		t.Fatalf("the purge kept a snapshot that holds the bank: %v", err)
	}
	if _, err := s.ctlTry("POST", "/snapshots/"+snap+"/restore", nil); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("restore of a snapshot the purge deleted: %v", err)
	}
	if imp := s.ctl("GET", "/banks/bank-a/purge", nil); !strings.Contains(fmt.Sprint(imp["blockers"]), "only bank left") {
		t.Fatalf("purge preview of the last bank: %v", imp)
	}
	if _, err := s.ctlTry("POST", "/banks", map[string]any{"name": "Other", "id": "bank-b"}); err == nil || !strings.Contains(err.Error(), "removed bank") {
		t.Fatalf("create with a purged id: %v", err)
	}
	if err := s.app.Save(); err != nil {
		t.Fatal(err)
	}
	c := s.loadConfig()
	if c.Bank("bank-b") != nil {
		t.Fatal("the setup still lists the purged bank")
	}
	again, err := connect.New(c, connect.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range again.BankStatuses() {
		if st.ID == "bank-b" {
			t.Fatal("a purged bank came back after a restart")
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func blockedOn(app *connect.App, id, reason, text string) bool {
	r, err := app.Resource(setup.KindBank, id)
	if err != nil {
		return false
	}
	for _, c := range r.Conditions {
		if c.Type == setup.ConditionBlocked && c.Status == "True" && c.Reason == reason && strings.Contains(c.Message, text) {
			return true
		}
	}
	return false
}

func TestRemovalResumesAfterRestarts(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	cfg, err := initStack(t, "http://127.0.0.1:1/webhooks")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := connect.Reset(context.Background(), cfg.Dir, cfg.DatabaseURL); err != nil {
			t.Error(err)
		}
	})
	start := func() run {
		t.Helper()
		loaded, err := connect.LoadConfig(cfg.Dir)
		if err != nil {
			t.Fatal(err)
		}
		r := runOnce(t, loaded)
		var once sync.Once
		inner := r.stop
		r.stop = func() { once.Do(inner) }
		t.Cleanup(r.stop)
		return r
	}
	r := start()
	waitFor(t, "both banks to be active", func() bool {
		for _, b := range r.app.BankStatuses() {
			if b.Phase != connect.PhaseActive {
				return false
			}
		}
		return true
	})
	open := func(bank, name, phone, dob, username, naira string) string {
		c, err := r.app.CreateCustomer(bank, connect.CustomerRequest{Name: name, Phone: phone, DOB: dob, Username: username, Password: "beerus-test-" + username})
		if err != nil {
			t.Fatal(err)
		}
		acc, err := r.app.OpenAccount(bank, connect.AccountRequest{Customer: c.ID, Type: "current"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.app.Cash(bank, "deposit", connect.CashRequest{Account: acc.NUBAN, Amount: naira}); err != nil {
			t.Fatal(err)
		}
		return acc.NUBAN
	}
	from := open("bank-a", "Tunde Motors Ltd", "08032345678", "1979-07-02", "tunde", "50000")
	to := open("bank-b", "Chidi Okafor", "08034567890", "1995-05-30", "chidi", "1000")
	if tr, err := r.app.Transfer(connect.TransferRequest{FromBank: "bank-a", From: from, ToBank: "bank-b", To: to, Amount: 4_000_000, Key: "restart-1"}); err != nil || tr.Status != "success" {
		t.Fatalf("transfer: %+v %v", tr, err)
	}
	if _, err := r.app.PurgeBank("bank-b", false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "settle-inflight to wait", func() bool {
		return blockedOn(r.app, "bank-b", setup.ReasonSettlementPending, "to settle 1 transfer (net ₦40,000.00)")
	})
	if got := storedBank(t, cfg.Dir, "bank-b").Done; !slices.Equal(got, []string{setup.FinalizerStopRouting, setup.FinalizerRevokeConnections}) {
		t.Fatalf("finalizers done before the first restart: %v", got)
	}
	r.stop()

	r = start()
	waitFor(t, "the removal to resume and wait for settlement", func() bool {
		return blockedOn(r.app, "bank-b", setup.ReasonSettlementPending, "to settle 1 transfer")
	})
	if _, err := r.app.SettleNow(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the money decision", func() bool {
		return blockedOn(r.app, "bank-b", setup.ReasonMoneyDecisionNeeded, "holds ₦41,000.00 for 1 customer")
	})
	r.stop()

	r = start()
	waitFor(t, "the money decision after the second restart", func() bool {
		return blockedOn(r.app, "bank-b", setup.ReasonMoneyDecisionNeeded, "holds ₦41,000.00 for 1 customer")
	})
	spec := storedStatus(t, cfg.Dir).Applied
	spec.Spec.Banks = slices.DeleteFunc(spec.Spec.Banks, func(b setup.Bank) bool { return b.ID == "bank-b" })
	if _, err := r.app.Apply(spec, connect.ApplyOptions{Prune: true, Money: &setup.Removal{Money: setup.MoneyTransfer, To: "bank-a"}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the moves to wait for settlement", func() bool {
		return blockedOn(r.app, "bank-b", setup.ReasonSettlementPending, "to settle 1 transfer (net ₦41,000.00)")
	})
	if got := storedBank(t, cfg.Dir, "bank-b").Done; !slices.Equal(got, []string{setup.FinalizerStopRouting, setup.FinalizerRevokeConnections, setup.FinalizerSettleInflight, setup.FinalizerDecideMoney}) {
		t.Fatalf("finalizers done before the third restart: %v", got)
	}
	r.stop()

	r = start()
	defer r.stop()
	waitFor(t, "the moves to wait for settlement after the third restart", func() bool {
		return blockedOn(r.app, "bank-b", setup.ReasonSettlementPending, "to settle 1 transfer (net ₦41,000.00)")
	})
	if _, err := r.app.SettleNow(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "bank-b to be removed", func() bool {
		_, err := r.app.Resource(setup.KindBank, "bank-b")
		return err != nil
	})
	if ck, err := r.app.CheckSettlement(); err != nil || len(ck.Problems) != 0 {
		t.Fatalf("settlement after the restarts: %+v %v", ck, err)
	}
	if ck, err := r.app.VerifyBank(); err != nil || !ck.OK() {
		t.Fatalf("bank ledgers after the restarts: %v %v", err, ck)
	}
	final, err := connect.LoadConfig(cfg.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if rep, err := connect.Doctor(context.Background(), final, false); err != nil || !rep.Clean() || len(rep.Missing) != 0 {
		t.Fatalf("doctor: %+v %v", rep, err)
	}
}
