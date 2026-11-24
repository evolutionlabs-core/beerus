package contracttest

import (
	"bytes"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/aggregator"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

func TestPartnerAPI(t *testing.T) {
	s := sharedStack(t)

	s.run("authentication", func(s *Stack) {
		const missing = "Send your Beerus-Client-ID and Beerus-Secret headers."
		const mismatch = "The client ID and secret do not match."
		for _, c := range []struct{ name, clientID, secret, message string }{
			{"no headers", "", "", missing},
			{"no secret", "balance", "", missing},
			{"no client ID", "", s.secret, missing},
			{"wrong secret", "balance", "bk_test_wrong", mismatch},
			{"unknown client ID", "nobody", s.secret, mismatch},
		} {
			r := s.call("GET", "/v1/webhook_keys", nil, credentials(c.clientID, c.secret), skipValidation())
			if r.Status != 401 || r.Code() != "unauthenticated" || !strings.Contains(string(r.Body), c.message) {
				t.Errorf("%s: %d %s, want 401 %q", c.name, r.Status, r.Body, c.message)
			}
		}
		s.must(s.call("GET", "/v1/webhook_keys", nil), 200)
		if r := s.call("GET", "/v1/nothing", nil, skipValidation()); r.Status != 404 || r.Code() != "not_found" {
			t.Errorf("unknown path: %d %s", r.Status, r.Body)
		}
	})

	s.run("institutions", func(s *Stack) {
		all := s.must(s.call("GET", "/v1/institutions", nil), 200).JSON["institutions"].([]any)
		ids := []string{}
		for _, in := range all {
			ids = append(ids, fmt.Sprint(in.(map[string]any)["institution_id"]))
		}
		if !slices.Contains(ids, "bank-a") || !slices.Contains(ids, "bank-b") {
			t.Fatalf("institutions %v", ids)
		}
		paged, cursor := []string{}, ""
		for range len(ids) + 1 {
			r := s.must(s.call("GET", "/v1/institutions?limit=1&cursor="+cursor, nil), 200)
			page := r.JSON["institutions"].([]any)
			if len(page) != 1 {
				t.Fatalf("page of one: %s", r.Body)
			}
			paged = append(paged, fmt.Sprint(page[0].(map[string]any)["institution_id"]))
			if cursor, _ = r.JSON["next_cursor"].(string); cursor == "" {
				break
			}
		}
		if !slices.Equal(paged, ids) {
			t.Fatalf("paging gave %v, want %v", paged, ids)
		}
		for _, c := range []struct{ name, path, code, message string }{
			{"bad limit", "/v1/institutions?limit=0", "invalid_request", "limit must be a number from 1 to 500."},
			{"bad cursor", "/v1/institutions?cursor=nope", "invalid_cursor", "That cursor is not valid. Use the next_cursor value from the last page."},
		} {
			r := s.call("GET", c.path, nil)
			if r.Status != 400 || r.Code() != c.code || !strings.Contains(string(r.Body), c.message) {
				t.Errorf("%s: %d %s, want 400 %s %q", c.name, r.Status, r.Body, c.code, c.message)
			}
		}
	})

	s.run("link tokens", func(s *Stack) {
		for _, c := range []struct {
			name    string
			body    map[string]any
			status  int
			code    string
			message string
		}{
			{"no user", map[string]any{"redirect_uri": s.redirect}, 422, "invalid_request", "user_id is required."},
			{"unknown bank", map[string]any{"user_id": "w", "institution_id": "bank-z", "redirect_uri": s.redirect}, 422, "invalid_request", "There is no bank bank-z in this setup. Use an institution_id from GET /v1/institutions."},
			{"other redirect", map[string]any{"user_id": "w", "redirect_uri": s.redirect + "/extra"}, 400, "invalid_request", "redirect_uri must exactly match a registered redirect URI."},
			{"user and bank", map[string]any{"user_id": "w", "institution_id": "bank-a", "redirect_uri": s.redirect}, 201, "", ""},
		} {
			r := s.call("POST", "/v1/link/token/create", c.body)
			if r.Status != c.status || r.Code() != c.code || !strings.Contains(string(r.Body), c.message) {
				t.Errorf("%s: %d %s, want %d %s %q", c.name, r.Status, r.Body, c.status, c.code, c.message)
			}
		}
		if r := s.call("POST", "/v1/link/token/create", map[string]any{"user_id": "w", "redirect_uri": s.redirect}, noIdem()); r.Status != 400 {
			t.Errorf("missing Idempotency-Key: %d", r.Status)
		}
		lt := s.must(s.call("POST", "/v1/link/token/create", map[string]any{"user_id": "w", "redirect_uri": s.redirect}), 201)
		if !strings.HasPrefix(fmt.Sprint(lt.JSON["link_token"]), "lt_") || lt.JSON["expiration"] == nil || len(lt.JSON) != 2 {
			t.Errorf("link token response: %s", lt.Body)
		}
	})

	s.run("idempotency", func(s *Stack) {
		body := map[string]any{"user_id": "ws_idem", "redirect_uri": s.redirect}
		key := randomKey()
		a := s.must(s.call("POST", "/v1/link/token/create", body, withKey(key)), 201)
		b := s.must(s.call("POST", "/v1/link/token/create", body, withKey(key)), 201)
		if !bytes.Equal(a.Body, b.Body) || b.Header.Get("Idempotent-Replayed") != "true" {
			t.Fatalf("replay differs or is not marked:\n%s\n%s", a.Body, b.Body)
		}
		body["user_id"] = "ws_other"
		if r := s.call("POST", "/v1/link/token/create", body, withKey(key)); r.Status != 422 || r.Code() != "idempotency_key_reused" {
			t.Errorf("reused key: %d %s", r.Status, r.Body)
		}
		if c := s.must(s.call("POST", "/v1/link/token/create", body), 201); c.JSON["link_token"] == a.JSON["link_token"] {
			t.Error("a new key returned the old token")
		}
	})

	s.run("exchange errors", func(s *Stack) {
		res := s.completeLink(s.linkURL("w", ""), "bank-a", "tunde", nil)
		exchange := func(token string) Resp {
			return s.call("POST", "/v1/public_token/exchange", map[string]any{"public_token": token})
		}
		ok := s.must(exchange(res.PublicToken), 200)
		if !strings.HasPrefix(fmt.Sprint(ok.JSON["access_token"]), "access-") || !strings.HasPrefix(fmt.Sprint(ok.JSON["connection_id"]), "con_") {
			t.Fatalf("exchange body: %s", ok.Body)
		}
		for _, c := range []struct {
			name, token, message string
		}{
			{"unknown", "public-nope", "This public_token is not known."},
			{"reused", res.PublicToken, "This public_token was already used."},
		} {
			if r := exchange(c.token); r.Status != 404 || r.Code() != "invalid_public_token" || !strings.Contains(string(r.Body), c.message) {
				t.Errorf("%s: %d %s, want 404 %q", c.name, r.Status, r.Body, c.message)
			}
		}
		view := s.must(s.call("GET", "/v1/link_sessions/"+res.SessionID, nil), 200)
		if view.JSON["status"] != "exchanged" || view.JSON["connection_id"] == nil {
			t.Errorf("exchanged session view: %s", view.Body)
		}
		link := s.linkURL("w", "")
		b := s.browser()
		b.get(link)
		b.post("/link/cancel", map[string][]string{})
		if b.result == nil || b.result.Query().Get("error") != "access_denied" || b.result.Query().Get("link_session_id") == "" {
			t.Fatalf("cancel redirect: %v", b.result)
		}
		if code := b.get(link); code != 409 && b.result == nil {
			t.Errorf("reused link token: %d", code)
		}
		conn := s.connect("tunde", "bank-a")
		id := s.id(conn, "current")
		r := s.call("GET", "/v1/accounts/"+id+"/transactions/sync?cursor=cur_garbage", nil, withToken(conn.AccessToken))
		if r.Status != 400 || r.Code() != "invalid_cursor" {
			t.Errorf("garbage cursor: %d %s", r.Status, r.Body)
		}
	})

	s.run("partners", func(s *Stack) {
		s.needsNodes()
		conn := s.connect("tunde", "bank-a")
		oldSecret := s.secret
		other := fmt.Sprint(s.ctl("POST", "/partners", map[string]any{"id": "ledgerly", "redirect_uris": []string{"https://ledgerly.localhost/cb"}})["secret"])
		if _, err := s.ctlTry("POST", "/partners", map[string]any{"id": "ledgerly", "redirect_uris": []string{"https://x.localhost/cb"}}); err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Errorf("a duplicate partner was accepted: %v", err)
		}
		if _, err := s.ctlTry("POST", "/partners", map[string]any{"id": "Bad ID", "redirect_uris": []string{"https://x.localhost/cb"}}); err == nil || !strings.Contains(err.Error(), "HTTP 422") {
			t.Errorf("an invalid partner ID was accepted: %v", err)
		}
		for _, c := range []struct {
			what, id, secret, token, path string
			want                          int
		}{
			{"a new partner", "ledgerly", other, "", "/v1/webhook_keys", 200},
			{"another partner's connection", "ledgerly", other, conn.AccessToken, "/v1/connection", 401},
			{"the owner's connection", "balance", oldSecret, conn.AccessToken, "/v1/connection", 200},
		} {
			if got := rawGet(s, c.id, c.secret, c.token, c.path); got != c.want {
				t.Errorf("%s: %d, want %d", c.what, got, c.want)
			}
		}
		newSecret := fmt.Sprint(s.ctl("POST", "/partners/balance/rotate-key", nil)["secret"])
		s.secret = newSecret
		if got := rawGet(s, "balance", oldSecret, "", "/v1/webhook_keys"); got != 401 {
			t.Errorf("the old secret after rotation got %d", got)
		}
		if got := rawGet(s, "balance", newSecret, conn.AccessToken, "/v1/connection"); got != 200 {
			t.Errorf("the new secret got %d", got)
		}
		s.ctl("DELETE", "/partners/ledgerly", nil)
		saved := s.loadConfig()
		if saved.Partner("ledgerly") != nil || saved.Partner("balance").SecretSHA256 != config.HashKey(newSecret) {
			t.Error("the partner changes were not saved")
		}
		if got := rawGet(s, "ledgerly", other, "", "/v1/webhook_keys"); got == 200 {
			t.Error("a removed partner still has access")
		}
	})

	s.run("pick accounts", func(s *Stack) {
		partner := s.cfg.Partners[0].ID
		for _, tc := range []struct {
			name     string
			pick     func([]string) []string
			shared   int
			rejected bool
		}{
			{"none picked", func([]string) []string { return nil }, 1, true},
			{"one picked", func(all []string) []string { return all[:1] }, 1, false},
			{"all picked", nil, 2, false},
		} {
			offered, picked := []string{}, []string{}
			b := s.browser()
			b.picked = func(all []string) []string {
				offered, picked = all, all
				if tc.pick != nil {
					picked = tc.pick(all)
				}
				return picked
			}
			b.get(s.linkURL("pick-"+tc.name, ""))
			b.post("/link/institution", map[string][]string{"institution": {"bank-a"}})
			b.bankSignIn("adaeze", nil)
			if tc.rejected {
				if b.result != nil || !strings.Contains(b.last, "Pick at least one account to continue.") || !strings.Contains(b.last, " disabled>Continue") {
					t.Fatalf("%s: the screen did not stay with Continue disabled: %.600s", tc.name, b.last)
				}
				for _, kind := range []string{"current", "savings"} {
					if !strings.Contains(b.last, `<span class="capitalize">`+kind+`</span> · <span class="font-mono">•••• `) {
						t.Fatalf("%s: the screen does not show the %s account type: %.600s", tc.name, kind, b.last)
					}
				}
				b.picked = func(all []string) []string {
					picked = all[:1]
					return picked
				}
				b.pickAccounts()
			}
			if b.result == nil || b.result.Query().Get("public_token") == "" {
				t.Fatalf("%s: no public_token on the redirect: %.300s", tc.name, b.last)
			}
			session := s.must(s.call("GET", "/v1/link_sessions/"+b.result.Query().Get("link_session_id"), nil), 200)
			if session.JSON["status"] != "authorised" {
				t.Fatalf("%s: the link session is %v: %s", tc.name, session.JSON["status"], session.Body)
			}
			conn := s.must(s.call("POST", "/v1/public_token/exchange", map[string]any{"public_token": b.result.Query().Get("public_token")}), 200)
			token := fmt.Sprint(conn.JSON["access_token"])
			s.syncNow()
			if listed := s.must(s.call("GET", "/v1/accounts", nil, withToken(token)), 200).JSON["data"].([]any); len(listed) != tc.shared {
				t.Fatalf("%s: connection lists %d accounts, want %d", tc.name, len(listed), tc.shared)
			}
			for _, nuban := range offered {
				r := s.call("GET", "/v1/accounts/"+aggregator.AccountID(partner, "bank-a", nuban)+"/balance?as_of="+s.linkStart().AddDays(-1).String(), nil, withToken(token))
				if slices.Contains(picked, nuban) {
					if r.Status == http.StatusNotFound {
						t.Errorf("%s: picked account %s gave 404", tc.name, nuban)
					}
					continue
				}
				if r.Status != http.StatusNotFound || r.Code() != "not_found" || fmt.Sprint(r.JSON["error"].(map[string]any)["message"]) != "No such account." {
					t.Errorf("%s: unpicked account %s: %d %s", tc.name, nuban, r.Status, r.Body)
				}
			}
		}
	})
}

func TestConnectionLifecycle(t *testing.T) {
	s := sharedStack(t)

	s.run("revoke and reconnect", func(s *Stack) {
		s.needsNodes()
		conn := s.connect("adaeze", "bank-a")
		id := s.id(conn, "current")
		before, _ := s.syncAll(conn.AccessToken, id, "", 500)
		s.ctl("POST", "/connections/"+conn.ConnectionID+"/revoke", nil)
		if got := s.status(conn.AccessToken); got != "revoked" {
			t.Fatalf("status after revoke: %v", got)
		}
		for _, r := range []Resp{
			s.call("GET", "/v1/accounts/"+id+"/transactions/sync", nil, withToken(conn.AccessToken)),
			s.call("POST", "/v1/connection/reauth", nil, withToken(conn.AccessToken)),
		} {
			if r.Status != 409 || r.Code() != "connection_revoked" {
				t.Errorf("after revoke: %d %s", r.Status, r.Body)
			}
		}
		if !s.hasRevoked(conn.ConnectionID) {
			t.Error("no revoked webhook")
		}
		again := s.connect("adaeze", "bank-a")
		if again.ConnectionID == conn.ConnectionID || s.id(again, "current") != id {
			t.Fatalf("reconnect gave connection %s and account %s, want a new connection and account %s", again.ConnectionID, s.id(again, "current"), id)
		}
		after, _ := s.syncAll(again.AccessToken, id, before.cursor, 500)
		for lid, l := range after.lines {
			if _, ok := before.lines[lid]; ok || l["status"] != "posted" {
				continue
			}
			if bt, _ := time.Parse(time.RFC3339, fmt.Sprint(l["booked_at"])); clock.DateOf(bt).Before(s.today()) {
				t.Fatalf("an old line came back with a new ID: %v", l)
			}
		}
		s.must(s.call("POST", "/v1/connections/remove", nil, withToken(again.AccessToken)), 204)
		if r := s.call("POST", "/v1/connections/remove", nil, withToken(again.AccessToken)); r.Status != 401 || r.Code() != "invalid_access_token" {
			t.Errorf("remove again: %d %s", r.Status, r.Body)
		}
	})

	s.run("reauth", func(s *Stack) {
		conn := s.connect("tunde", "bank-a")
		id := fmt.Sprint(conn.Accounts[0]["id"])
		ls := s.must(s.call("POST", "/v1/connection/reauth", map[string]string{"state": "again"}, withToken(conn.AccessToken)), 201)
		if ls.JSON["connection_id"] != conn.ConnectionID {
			t.Fatalf("reauth session: %s", ls.Body)
		}
		res := s.completeLink(fmt.Sprint(ls.JSON["widget_url"]), "", "tunde", nil)
		if res.PublicToken == "" || res.State != "again" {
			t.Fatalf("reauth link: %+v", res)
		}
		mark := len(s.events())
		ex := s.must(s.call("POST", "/v1/public_token/exchange", map[string]any{"public_token": res.PublicToken}), 200)
		if _, ok := ex.JSON["access_token"]; ok || ex.JSON["connection_id"] != conn.ConnectionID {
			t.Fatalf("reauth exchange: %s", ex.Body)
		}
		before, _ := s.syncAll(conn.AccessToken, id, "", 500)
		s.spend(s.persona("tunde"), 2)
		s.syncNow()
		if got := s.status(conn.AccessToken); got != "active" {
			t.Fatalf("status after reauth: %v", got)
		}
		waitFor(t, waitLimit, "sync.available after the reauth", func() bool {
			return slices.ContainsFunc(s.events()[mark:], func(e map[string]any) bool { return e["type"] == "sync.available" })
		})
		had := len(before.lines)
		if after, _ := s.syncMore(conn.AccessToken, id, before, 500); len(after.lines) <= had {
			t.Error("no new lines after two payments")
		}
		accts := s.must(s.call("GET", "/v1/accounts", nil, withToken(conn.AccessToken)), 200)
		if fmt.Sprint(accts.JSON["data"].([]any)[0].(map[string]any)["id"]) != id {
			t.Error("account ID changed after reauth")
		}
	})

	s.run("revoked connections stay revoked", func(s *Stack) {
		s.needsNodes()
		conn := s.connect("tunde", "bank-a")
		ls := s.must(s.call("POST", "/v1/connection/reauth", nil, withToken(conn.AccessToken)), 201)
		s.must(s.call("POST", "/v1/connections/remove", nil, withToken(conn.AccessToken)), 204)
		if res := s.completeLink(fmt.Sprint(ls.JSON["widget_url"]), "", "tunde", nil); res.Error != "access_denied" || res.PublicToken != "" {
			t.Errorf("widget let a removed connection be reauthorised: %+v", res)
		}
		conn2 := s.connect("tunde", "bank-a")
		ls2 := s.must(s.call("POST", "/v1/connection/reauth", nil, withToken(conn2.AccessToken)), 201)
		res2 := s.completeLink(fmt.Sprint(ls2.JSON["widget_url"]), "", "tunde", nil)
		s.ctl("POST", "/connections/"+conn2.ConnectionID+"/revoke", nil)
		if r := s.call("POST", "/v1/public_token/exchange", map[string]any{"public_token": res2.PublicToken}); r.Status != 409 || r.Code() != "connection_revoked" {
			t.Errorf("exchange after revoke: %d %s", r.Status, r.Body)
		}
		if r := s.call("GET", "/v1/connection", nil, withToken(conn.AccessToken)); r.Status != 401 {
			t.Errorf("removed connection answered %d", r.Status)
		}
		if got := s.status(conn2.AccessToken); got != "revoked" {
			t.Errorf("connection %s came back as %v", conn2.ConnectionID, got)
		}
	})

	s.run("one revoke leaves others working", func(s *Stack) {
		a := s.connect("tunde", "bank-a")
		res := s.completeLink(s.linkURL("ws_other", "x"), "bank-a", "tunde", nil)
		b := s.must(s.call("POST", "/v1/public_token/exchange", map[string]any{"public_token": res.PublicToken}), 200)
		s.syncNow()
		acc := s.must(s.call("GET", "/v1/accounts", nil, withToken(a.AccessToken)), 200).JSON["data"].([]any)[0].(map[string]any)
		s.must(s.call("POST", "/v1/connections/remove", nil, withToken(fmt.Sprint(b.JSON["access_token"]))), 204)
		s.must(s.call("GET", "/v1/accounts/"+fmt.Sprint(acc["id"])+"/transactions/sync", nil, withToken(a.AccessToken)), 200)
		got := s.must(s.call("GET", "/v1/accounts/"+fmt.Sprint(acc["id"]), nil, withToken(a.AccessToken)), 200)
		if got.JSON["connection_id"] != a.ConnectionID || acc["connection_id"] != a.ConnectionID {
			t.Errorf("accounts report connection %v and %v, want %v", acc["connection_id"], got.JSON["connection_id"], a.ConnectionID)
		}
	})

	s.run("refresh and rate limit", func(s *Stack) {
		conn := s.connect("ngozi", "bank-b")
		id := fmt.Sprint(conn.Accounts[0]["id"])
		ref := fmt.Sprint(s.must(s.call("POST", "/v1/accounts/"+id+"/refresh", nil, withToken(conn.AccessToken)), 202).JSON["id"])
		if r := s.call("POST", "/v1/accounts/"+id+"/refresh", nil, withToken(conn.AccessToken)); r.Status != 429 || r.Code() != "rate_limited" || r.Header.Get("Retry-After") == "" {
			t.Errorf("second refresh: %d %s", r.Status, r.Body)
		}
		waitFor(t, 10*time.Second, "refresh.completed", func() bool {
			return len(s.eventsOf("refresh.completed", func(d map[string]any) bool { return d["refresh_id"] == ref && d["outcome"] == "succeeded" })) > 0
		})
	})
}
