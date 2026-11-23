package contracttest

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/aggregator"
)

func TestPickAccounts(t *testing.T) {
	s := newStack(t)
	partner := s.cfg.Partners[0].ID
	cases := []struct {
		name     string
		pick     func([]string) []string
		shared   int
		rejected bool
	}{
		{"all picked", nil, 2, false},
		{"one picked", func(all []string) []string { return all[:1] }, 1, false},
		{"none picked", func([]string) []string { return nil }, 0, true},
	}
	for _, tc := range cases {
		offered, picked := []string{}, []string{}
		ls := s.must(s.call("POST", "/v1/link/token/create", map[string]any{"user_id": "pick-" + tc.name, "redirect_uri": s.redirect}), 201)
		b := s.browser()
		b.picked = func(all []string) []string {
			offered = all
			picked = all
			if tc.pick != nil {
				picked = tc.pick(all)
			}
			return picked
		}
		b.get(fmt.Sprint(ls.JSON["widget_url"]))
		b.post("/link/institution", map[string][]string{"institution": {"bank-a"}})
		b.bankSignIn("adaeze", nil)
		session := s.must(s.call("GET", "/v1/link_sessions/"+fmt.Sprint(ls.JSON["id"]), nil), 200)
		if tc.rejected {
			if b.result != nil || !strings.Contains(b.last, "Pick at least one account to continue.") || !strings.Contains(b.last, " disabled>Continue") {
				t.Fatalf("%s: the screen did not stay with Continue disabled: %.600s", tc.name, b.last)
			}
			if session.JSON["connection_id"] != nil || session.JSON["status"] != "picking" {
				t.Fatalf("%s: the link session moved on: %s", tc.name, session.Body)
			}
			continue
		}
		if b.result == nil || b.result.Query().Get("public_token") == "" {
			t.Fatalf("%s: no public_token on the redirect: %.300s", tc.name, b.last)
		}
		conn := s.must(s.call("POST", "/v1/public_token/exchange", map[string]any{"public_token": b.result.Query().Get("public_token")}), 200)
		token := fmt.Sprint(conn.JSON["access_token"])
		s.syncNow()
		listed := s.must(s.call("GET", "/v1/accounts", nil, withToken(token)), 200).JSON["data"].([]any)
		if len(listed) != tc.shared {
			t.Fatalf("%s: connection lists %d accounts, want %d", tc.name, len(listed), tc.shared)
		}
		for _, nuban := range offered {
			r := s.call("GET", "/v1/accounts/"+aggregator.AccountID(partner, "bank-a", nuban)+"/balance", nil, withToken(token))
			if slices.Contains(picked, nuban) {
				if r.Status == http.StatusNotFound {
					t.Fatalf("%s: picked account %s gave 404", tc.name, nuban)
				}
				continue
			}
			if r.Status != http.StatusNotFound || r.Code() != "not_found" || fmt.Sprint(r.JSON["error"].(map[string]any)["message"]) != "No such account." {
				t.Fatalf("%s: unpicked account %s: %d %s", tc.name, nuban, r.Status, r.Body)
			}
		}
	}
}
