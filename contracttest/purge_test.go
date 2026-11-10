package contracttest

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/connect"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func TestPurgeLeavesNothingBehind(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newBankStack(t)
	s.ctl("POST", "/banks", map[string]any{"name": "Abuja Digital", "type": "digital"})
	s.waitPhase("abuja-digital", connect.PhaseActive, 60*time.Second)
	bank := *s.cfg.Bank("abuja-digital")
	home := s.persona("adaeze")
	s.pay("bank-a", home.Accounts[0], "abuja-digital", s.openAccount(s.join("adaeze", "abuja-digital"), "current"), "25000")
	conn := s.connect("adaeze", "abuja-digital", s.today().AddDays(-3))
	acct := fmt.Sprint(conn.Accounts[0]["id"])
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
	if _, err := s.ctlTry("POST", "/banks/abuja-digital/purge", nil); err == nil || !strings.Contains(err.Error(), "HTTP 422") || !strings.Contains(err.Error(), "close it before purging") {
		t.Fatalf("purge an open bank: %v", err)
	}
	s.ctl("POST", "/banks/abuja-digital/close", map[string]any{"money": "transfer", "to": "bank-a"})
	if _, err := s.ctlTry("POST", "/banks/abuja-digital/purge", nil); err == nil || !strings.Contains(err.Error(), "2 NIP sessions with this bank have not settled") {
		t.Fatalf("purge before the closure transfer settled: %v", err)
	}
	s.ctl("POST", "/nip/settle-now", nil)
	imp := s.ctl("GET", "/banks/abuja-digital/purge", nil)
	if len(imp["blockers"].([]any)) != 0 || fmt.Sprint(imp["snapshots"]) != "["+snap+"]" || imp["database"] != bank.Database || imp["connections"] != float64(1) {
		t.Fatalf("purge preview: %v", imp)
	}
	res := s.ctl("POST", "/banks/abuja-digital/purge", nil)
	if fmt.Sprint(res["snapshots_deleted"]) != "["+snap+"]" {
		t.Fatalf("purge: %v", res)
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
	if imp := s.ctl("GET", "/banks/bank-a/purge", nil); !strings.Contains(fmt.Sprint(imp["blockers"]), "close it before purging") {
		t.Fatalf("purge preview of an open bank: %v", imp)
	}
	s.ctl("POST", "/banks/bank-b/close", map[string]any{"money": "leave"})
	snap := fmt.Sprint(s.ctl("POST", "/snapshots", map[string]string{"name": "closed"})["id"])
	if res := s.ctl("POST", "/banks/bank-b/purge", nil); fmt.Sprint(res["snapshots_deleted"]) != "["+snap+"]" {
		t.Fatalf("the purge kept a snapshot that holds the bank: %v", res)
	}
	if _, err := s.ctlTry("POST", "/snapshots/"+snap+"/restore", nil); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("restore of a snapshot the purge deleted: %v", err)
	}
	if imp := s.ctl("GET", "/banks/bank-a/purge", nil); !strings.Contains(fmt.Sprint(imp["blockers"]), "only bank left") {
		t.Fatalf("purge preview of the last bank: %v", imp)
	}
	if _, err := s.ctlTry("POST", "/banks", map[string]any{"name": "Other", "id": "bank-b"}); err == nil || !strings.Contains(err.Error(), "purged bank") {
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
