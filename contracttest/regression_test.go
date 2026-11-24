package contracttest

import (
	"context"
	"fmt"
	"testing"
)

func TestRevokedConnectionCannotBeReauthorised(t *testing.T) {
	s := newStack(t)
	conn := s.connect("tunde", "bank-a")
	ls := s.must(s.call("POST", "/v1/connections/"+conn.ConnectionID+"/reauth", nil), 201)
	s.must(s.call("DELETE", "/v1/connections/"+conn.ConnectionID, nil), 204)
	res := s.completeLink(fmt.Sprint(ls.JSON["widget_url"]), "", "tunde", nil)
	if res.Error != "access_denied" || res.Code != "" {
		t.Fatalf("widget let a revoked connection be reauthorised: %+v", res)
	}
	conn2 := s.connect("tunde", "bank-a")
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

func TestOneRevokeLeavesOtherConnectionsWorking(t *testing.T) {
	s := newStack(t)
	a := s.connect("tunde", "bank-a")
	res := s.completeLink(s.linkURL("ws_other", "x"), "bank-a", "tunde", nil)
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
	conn := s.connect("tunde", "bank-a")
	id := fmt.Sprint(conn.Accounts[0]["id"])
	st, _ := s.syncAll(id, "", 500)
	ls := s.must(s.call("POST", "/v1/connections/"+conn.ConnectionID+"/reauth", nil), 201)
	res := s.completeLink(fmt.Sprint(ls.JSON["widget_url"]), "", "tunde", nil)
	mark := len(s.events())
	s.must(s.call("POST", "/v1/connections/exchange", map[string]any{"code": res.Code, "link_session_id": res.SessionID}), 200)
	s.spend(s.persona("tunde"), 2)
	s.syncNow()
	waitFor(t, waitLimit, "sync.available after the reauth", func() bool {
		for _, e := range s.events()[mark:] {
			if e["type"] == "sync.available" {
				return true
			}
		}
		return false
	})
	before := len(st.lines)
	after, _ := s.syncMore(id, st, 500)
	if len(after.lines) <= before {
		t.Fatal("no new lines after two payments")
	}
}

func TestBankLedgerStaysConsistent(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newBankStack(t)
	s.connect("adaeze", "bank-a")
	s.pay("bank-a", s.persona("adaeze").Accounts[0], "bank-b", s.persona("ngozi").Accounts[0], "20000")
	c, err := s.app.VerifyBank(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !c.OK() {
		t.Fatalf("bank ledger: %v", c)
	}
}
