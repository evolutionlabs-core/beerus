package contracttest

import (
	"fmt"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

func TestRevokedConnectionCannotBeReauthorised(t *testing.T) {
	s := newStack(t)
	conn := s.connect("tunde", "bank-a", s.today().AddDays(-30))
	ls := s.must(s.call("POST", "/v1/connections/"+conn.ConnectionID+"/reauth", nil), 201)
	s.must(s.call("DELETE", "/v1/connections/"+conn.ConnectionID, nil), 204)
	res := s.completeLink(fmt.Sprint(ls.JSON["widget_url"]), "", "tunde", nil)
	if res.Error != "access_denied" || res.Code != "" {
		t.Fatalf("widget let a revoked connection be reauthorised: %+v", res)
	}
	conn2 := s.connect("tunde", "bank-a", s.today().AddDays(-30))
	ls2 := s.must(s.call("POST", "/v1/connections/"+conn2.ConnectionID+"/reauth", nil), 201)
	res2 := s.completeLink(fmt.Sprint(ls2.JSON["widget_url"]), "", "tunde", nil)
	s.ctl("POST", "/connections/"+conn2.ConnectionID+"/revoke", nil)
	r := s.call("POST", "/v1/connections/exchange", map[string]any{"code": res2.Code, "link_session_id": res2.SessionID})
	if r.Status != 409 || r.Code() != "connection_revoked" {
		t.Fatalf("exchange after revoke: %d %s", r.Status, r.Body)
	}
	for _, id := range []string{conn.ConnectionID, conn2.ConnectionID} {
		c := s.must(s.call("GET", "/v1/connections/"+id, nil), 200)
		if c.JSON["status"] != "revoked" {
			t.Fatalf("connection %s came back as %v", id, c.JSON["status"])
		}
	}
	if !testingExternal() {
		a := s.call("GET", "/v1/accounts/"+fmt.Sprint(conn.Accounts[0]["id"]), nil)
		if a.Status != 409 || a.Code() != "connection_revoked" {
			t.Fatalf("account read after revoke: %d %s", a.Status, a.Body)
		}
	}
}

func TestReconnectWithLaterStartKeepsBalanceInvariant(t *testing.T) {
	s := newStack(t)
	c1 := s.connect("adaeze", "bank-a", s.today().AddDays(-30))
	s.spend(s.persona("adaeze"), 3)
	s.advance("48h")
	s.spend(s.persona("adaeze"), 2)
	s.syncNow()
	s.ctl("POST", "/connections/"+c1.ConnectionID+"/revoke", nil)
	s.spend(s.persona("adaeze"), 2)
	s.advance("48h")
	start := s.today().AddDays(-1)
	c2 := s.connect("adaeze", "bank-a", start)
	id := fmt.Sprint(accountAt(t, c2, "current")["id"])
	hist := s.eventsOf("historical_sync.completed", func(d map[string]any) bool {
		return d["connection_id"] == c2.ConnectionID && d["account_id"] == id
	})
	if len(hist) == 0 {
		t.Fatal("no historical_sync.completed for the new connection")
	}
	eff := fmt.Sprint(hist[len(hist)-1]["effective_start_date"])
	first, err := clock.ParseDate(eff)
	if err != nil || !first.Before(start) {
		t.Fatalf("effective_start_date %q should be before the requested start %s", eff, start)
	}
	feed, _ := s.syncAll(id, "", 500)
	var sum int64
	for _, l := range feed.lines {
		if l["status"] == "posted" {
			sum += num(l["amount_minor"])
		}
	}
	bal := s.must(s.call("GET", "/v1/accounts/"+id+"/balance?as_of="+first.AddDays(-1).String(), nil), 200)
	if bal.JSON["effective_start_date"] != eff {
		t.Fatalf("balance effective_start_date %v, webhook %s", bal.JSON["effective_start_date"], eff)
	}
	opening := num(bal.JSON["ledger_balance"].(map[string]any)["amount_minor"])
	cur := s.must(s.call("GET", "/v1/accounts/"+id, nil), 200)
	ledger := num(cur.JSON["balances"].(map[string]any)["current"].(map[string]any)["amount_minor"])
	if opening+sum != ledger {
		t.Fatalf("opening %d + posted %d != ledger %d", opening, sum, ledger)
	}
}

func TestOneRevokeLeavesOtherConnectionsWorking(t *testing.T) {
	s := newStack(t)
	a := s.connect("tunde", "bank-a", s.today().AddDays(-30))
	ls := s.must(s.call("POST", "/v1/link_sessions", map[string]any{"start_date": s.today().AddDays(-30).String(), "workspace_id": "ws_other", "redirect_uri": s.redirect, "state": "x"}), 201)
	res := s.completeLink(fmt.Sprint(ls.JSON["widget_url"]), "bank-a", "tunde", nil)
	b := s.must(s.call("POST", "/v1/connections/exchange", map[string]any{"code": res.Code, "link_session_id": res.SessionID}), 201)
	s.syncNow()
	la := s.must(s.call("GET", "/v1/connections/"+a.ConnectionID+"/accounts", nil), 200)
	acc := la.JSON["data"].([]any)[0].(map[string]any)
	if acc["connection_id"] != a.ConnectionID {
		t.Fatalf("connection A lists its account under %v", acc["connection_id"])
	}
	s.must(s.call("DELETE", "/v1/connections/"+fmt.Sprint(b.JSON["id"]), nil), 204)
	s.must(s.call("GET", "/v1/accounts/"+fmt.Sprint(acc["id"])+"/transactions/sync", nil), 200)
	got := s.must(s.call("GET", "/v1/accounts/"+fmt.Sprint(acc["id"]), nil), 200)
	if got.JSON["connection_id"] != a.ConnectionID {
		t.Fatalf("account now reports connection %v", got.JSON["connection_id"])
	}
}

func TestReauthSendsSyncAvailable(t *testing.T) {
	s := newStack(t)
	conn := s.connect("tunde", "bank-a", s.today().AddDays(-30))
	id := fmt.Sprint(conn.Accounts[0]["id"])
	st, _ := s.syncAll(id, "", 500)
	s.advance("366d")
	s.spend(s.persona("tunde"), 2)
	s.advance("72h")
	ls := s.must(s.call("POST", "/v1/connections/"+conn.ConnectionID+"/reauth", nil), 201)
	res := s.completeLink(fmt.Sprint(ls.JSON["widget_url"]), "", "tunde", nil)
	mark := len(s.events())
	s.must(s.call("POST", "/v1/connections/exchange", map[string]any{"code": res.Code, "link_session_id": res.SessionID}), 200)
	s.syncNow()
	found := false
	for _, e := range s.events()[mark:] {
		if e["type"] == "sync.available" {
			found = true
		}
	}
	before := len(st.lines)
	after, _ := s.syncMore(id, st, 500)
	if len(after.lines) <= before {
		t.Fatal("no new lines after two payments")
	}
	if !found {
		t.Fatal("reauth brought new lines but sent no sync.available")
	}
}

func TestBankLedgerStaysConsistent(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newBankStack(t)
	s.connect("adaeze", "bank-a", s.today().AddDays(-40))
	s.pay("bank-a", s.persona("adaeze").Accounts[0], "bank-b", s.persona("ngozi").Accounts[0], "20000")
	s.advance("40d")
	c, err := s.app.VerifyBank()
	if err != nil {
		t.Fatal(err)
	}
	if !c.OK() {
		t.Fatalf("bank ledger: %v", c)
	}
}
