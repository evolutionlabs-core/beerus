package contracttest

import (
	"bytes"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

func accountAt(t *testing.T, c connected, typ string) map[string]any {
	t.Helper()
	for _, a := range c.Accounts {
		if a["type"] == typ {
			return a
		}
	}
	t.Fatalf("no %s account", typ)
	return nil
}

func TestAuthentication(t *testing.T) {
	s := newStack(t)
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
			t.Fatalf("%s: %d %s, want 401 %q", c.name, r.Status, r.Body, c.message)
		}
	}
	if code := rawGet(t, s, "balance", s.secret, "", "/v1/webhook_keys"); code != http.StatusOK {
		t.Fatalf("a call with the client ID and secret over plain HTTP got %d, want 200", code)
	}
	s.must(s.call("GET", "/v1/webhook_keys", nil), 200)
	r := s.call("GET", "/v1/nothing", nil, skipValidation())
	if r.Status != 404 || r.Code() != "not_found" {
		t.Fatalf("unknown path: %d %s", r.Status, r.Body)
	}
}

func TestInstitutions(t *testing.T) {
	s := newStack(t)
	for _, c := range []struct{ name, path, want string }{
		{"full list", "/v1/institutions", `{"institutions":[{"institution_id":"bank-a","name":"Bank A"},{"institution_id":"bank-b","name":"Bank B"}]}`},
		{"first page", "/v1/institutions?limit=1", `{"institutions":[{"institution_id":"bank-a","name":"Bank A"}],"next_cursor":"bank-a"}`},
		{"second page", "/v1/institutions?limit=1&cursor=bank-a", `{"institutions":[{"institution_id":"bank-b","name":"Bank B"}]}`},
	} {
		r := s.must(s.call("GET", c.path, nil), 200)
		if string(bytes.TrimSpace(r.Body)) != c.want {
			t.Fatalf("%s: %s, want %s", c.name, r.Body, c.want)
		}
	}
	for _, c := range []struct{ name, path, code, message string }{
		{"bad limit", "/v1/institutions?limit=0", "invalid_request", "limit must be a number from 1 to 500."},
		{"bad cursor", "/v1/institutions?cursor=nope", "invalid_cursor", "That cursor is not valid. Use the next_cursor value from the last page."},
	} {
		r := s.call("GET", c.path, nil)
		if r.Status != 400 || r.Code() != c.code || !strings.Contains(string(r.Body), c.message) {
			t.Fatalf("%s: %d %s, want 400 %s %q", c.name, r.Status, r.Body, c.code, c.message)
		}
	}
	r := s.call("GET", "/v1/institutions", nil, credentials("", ""), skipValidation())
	if r.Status != 401 || r.Code() != "unauthenticated" {
		t.Fatalf("no credentials: %d %s", r.Status, r.Body)
	}
}

func TestLinkTokenCreate(t *testing.T) {
	s := newStack(t)
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
		{"user only", map[string]any{"user_id": "w", "redirect_uri": s.redirect}, 201, "", ""},
		{"user and bank", map[string]any{"user_id": "w", "institution_id": "bank-a", "redirect_uri": s.redirect}, 201, "", ""},
	} {
		r := s.call("POST", "/v1/link/token/create", c.body)
		if r.Status != c.status || r.Code() != c.code || !strings.Contains(string(r.Body), c.message) {
			t.Errorf("%s: %d %s, want %d %s %q", c.name, r.Status, r.Body, c.status, c.code, c.message)
		}
	}
	r := s.call("POST", "/v1/link/token/create", map[string]any{"user_id": "w", "redirect_uri": s.redirect}, noIdem())
	if r.Status != 400 {
		t.Fatalf("missing Idempotency-Key: %d", r.Status)
	}
	lt := s.must(s.call("POST", "/v1/link/token/create", map[string]any{"user_id": "w", "redirect_uri": s.redirect}), 201)
	if !strings.HasPrefix(fmt.Sprint(lt.JSON["link_token"]), "lt_") || lt.JSON["expiration"] == nil || len(lt.JSON) != 2 {
		t.Fatalf("link token response: %s", lt.Body)
	}
}

func TestIdempotency(t *testing.T) {
	s := newStack(t)
	body := map[string]any{"user_id": "ws_idem", "redirect_uri": s.redirect}
	key := randomKey()
	a := s.must(s.call("POST", "/v1/link/token/create", body, withKey(key)), 201)
	b := s.must(s.call("POST", "/v1/link/token/create", body, withKey(key)), 201)
	if !bytes.Equal(a.Body, b.Body) {
		t.Fatalf("replay differs:\n%s\n%s", a.Body, b.Body)
	}
	if b.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatal("replay not marked")
	}
	body["user_id"] = "ws_other"
	r := s.call("POST", "/v1/link/token/create", body, withKey(key))
	if r.Status != 422 || r.Code() != "idempotency_key_reused" {
		t.Fatalf("reused key: %d %s", r.Status, r.Body)
	}
	c := s.must(s.call("POST", "/v1/link/token/create", body), 201)
	if c.JSON["link_token"] == a.JSON["link_token"] {
		t.Fatal("a new key returned the old token")
	}
}

func TestConnectAndSync(t *testing.T) {
	s := newBankStack(t)
	today := s.today()
	s.spend(s.persona("adaeze"), 4)
	conn := s.connect("adaeze", "bank-a")
	if len(conn.Accounts) != 2 {
		t.Fatalf("want 2 Bank A accounts, got %d", len(conn.Accounts))
	}
	acct := accountAt(t, conn, "current")
	id := fmt.Sprint(acct["id"])
	small, _ := s.syncAll(conn.AccessToken, id, "", 7)
	big, last := s.syncAll(conn.AccessToken, id, "", 500)
	if len(big.lines) != 17 {
		t.Fatalf("%d lines, want a deposit and 4 transfers of 4 lines", len(big.lines))
	}
	if small.pages < 2 || len(small.lines) != len(big.lines) {
		t.Fatalf("paging changed the result: %d pages, %d vs %d lines", small.pages, len(small.lines), len(big.lines))
	}
	for id, l := range big.lines {
		if fmt.Sprint(small.lines[id]) != fmt.Sprint(l) {
			t.Fatalf("line %s differs between page sizes", id)
		}
	}
	if last.JSON["has_more"] != false || last.JSON["world_id"] == "" {
		t.Fatalf("last page: %s", last.Body)
	}
	again, r := s.syncMore(conn.AccessToken, id, big, 100)
	if r.Status != 200 || len(r.JSON["added"].([]any))+len(r.JSON["modified"].([]any))+len(r.JSON["removed"].([]any)) != 0 || len(again.lines) != len(big.lines) {
		t.Fatalf("a second sync with no changes returned data: %s", r.Body)
	}

	posted := []map[string]any{}
	var sum int64
	for _, l := range big.lines {
		if l["status"] == "posted" {
			posted = append(posted, l)
			sum += num(l["amount_minor"])
			if l["booked_at"] == nil || l["balance_after_minor"] == nil {
				t.Fatalf("posted line without booking data: %v", l)
			}
		} else if l["booked_at"] != nil || l["balance_after_minor"] != nil {
			t.Fatalf("pending line with booking data: %v", l)
		}
	}
	sort.Slice(posted, func(i, j int) bool { return num(posted[i]["sequence"]) < num(posted[j]["sequence"]) })
	start := s.firstDate(id, conn.StartDate)
	open := s.must(s.call("GET", fmt.Sprintf("/v1/accounts/%s/balance?as_of=%s", id, start.AddDays(-1)), nil, withToken(conn.AccessToken)), 200)
	opening := num(open.JSON["ledger_balance"].(map[string]any)["amount_minor"])
	run := opening
	for _, l := range posted {
		run += num(l["amount_minor"])
		if num(l["balance_after_minor"]) != run {
			t.Fatalf("balance_after chain broken at %v", l["id"])
		}
	}
	got := s.must(s.call("GET", "/v1/accounts/"+id, nil, withToken(conn.AccessToken)), 200)
	current := num(got.JSON["balances"].(map[string]any)["current"].(map[string]any)["amount_minor"])
	if opening+sum != current {
		t.Fatalf("opening %d + posted %d != current %d", opening, sum, current)
	}
	yesterday := s.must(s.call("GET", fmt.Sprintf("/v1/accounts/%s/balance?as_of=%s", id, today.AddDays(-1)), nil, withToken(conn.AccessToken)), 200)
	var upto int64 = opening
	for _, l := range posted {
		bt, _ := time.Parse(time.RFC3339, fmt.Sprint(l["booked_at"]))
		if clock.DateOf(bt).Before(today) {
			upto += num(l["amount_minor"])
		}
	}
	if num(yesterday.JSON["ledger_balance"].(map[string]any)["amount_minor"]) != upto {
		t.Fatal("as_of yesterday does not match the posted lines")
	}

	kinds := map[string]bool{}
	for _, l := range big.lines {
		kinds[fmt.Sprint(l["description"])] = true
		if refs := l["references"].(map[string]any); refs["session_id"] != nil && len(fmt.Sprint(refs["session_id"])) != 30 {
			t.Fatalf("bad session id %v", refs["session_id"])
		}
	}
	for _, want := range []string{"Transfer fee", "VAT on transfer fee", "Stamp duty"} {
		if !kinds[want] {
			t.Errorf("no %q line", want)
		}
	}

	if s.sink != nil {
		pub := s.publicKey()
		hooks := s.sink.all()
		seen := map[string]bool{}
		for _, h := range hooks {
			if err := verifyV1a(pub, h, time.Now()); err != nil {
				t.Fatalf("webhook signature: %v", err)
			}
			var ev struct {
				Type string `json:"type"`
			}
			_ = jsonUnmarshal(h.Body, &ev)
			seen[ev.Type] = true
			validateWebhook(t, ev.Type, h.Body)
			for _, name := range []string{"webhook-id", "webhook-timestamp", "webhook-signature"} {
				if h.Header.Get(name) == "" {
					t.Errorf("webhook %s has no %s header", ev.Type, name)
				}
			}
		}
		for _, want := range []string{"account.added", "historical_sync.completed", "sync.available", "connection.status_changed"} {
			if !seen[want] {
				t.Errorf("no %s webhook", want)
			}
		}
	}
}

func TestBalanceRange(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an account no earlier consent covered")
	}
	s := newStack(t)
	today := s.today()
	start := s.linkStart()
	conn := s.connect("ngozi", "bank-b")
	id := fmt.Sprint(conn.Accounts[0]["id"])
	c := s.must(s.call("GET", "/v1/connection", nil, withToken(conn.AccessToken)), 200)
	if c.JSON["effective_start_date"] != nil {
		t.Fatalf("effective_start_date %v, want null", c.JSON["effective_start_date"])
	}
	ok := s.must(s.call("GET", fmt.Sprintf("/v1/accounts/%s/balance?as_of=%s", id, start.AddDays(-1)), nil, withToken(conn.AccessToken)), 200)
	if ok.JSON["effective_start_date"] != nil {
		t.Fatalf("balance effective_start_date: %s", ok.Body)
	}
	for _, d := range []clock.Date{start.AddDays(-2), today} {
		r := s.call("GET", fmt.Sprintf("/v1/accounts/%s/balance?as_of=%s", id, d), nil, withToken(conn.AccessToken))
		if r.Status != 422 || r.Code() != "as_of_out_of_range" {
			t.Errorf("as_of %s: %d %s", d, r.Status, r.Body)
		}
	}
	r := s.call("GET", "/v1/accounts/"+id+"/balance?as_of=yesterday", nil, withToken(conn.AccessToken))
	if r.Status != 400 {
		t.Errorf("bad as_of: %d", r.Status)
	}
}

func TestExchangeErrors(t *testing.T) {
	s := newStack(t)
	mk := func() linkResult {
		return s.completeLink(s.linkURL("w", ""), "bank-a", "tunde", nil)
	}
	r1 := mk()
	id1 := r1.SessionID
	exchange := func(token string) Resp {
		return s.call("POST", "/v1/public_token/exchange", map[string]any{"public_token": token})
	}
	ok := s.must(exchange(r1.PublicToken), 200)
	if !strings.HasPrefix(fmt.Sprint(ok.JSON["access_token"]), "access-") || !strings.HasPrefix(fmt.Sprint(ok.JSON["connection_id"]), "con_") {
		t.Fatalf("exchange body: %s", ok.Body)
	}
	rows := []struct {
		name    string
		token   string
		status  int
		message string
	}{
		{"unknown", "public-nope", 404, "This public_token is not known."},
		{"reused", r1.PublicToken, 404, "This public_token was already used."},
	}
	for _, c := range rows {
		r := exchange(c.token)
		if r.Status != c.status || r.Code() != "invalid_public_token" || !strings.Contains(string(r.Body), c.message) {
			t.Fatalf("%s: %d %s, want %d invalid_public_token %q", c.name, r.Status, r.Body, c.status, c.message)
		}
	}
	view := s.must(s.call("GET", "/v1/link_sessions/"+id1, nil), 200)
	if view.JSON["status"] != "exchanged" || view.JSON["connection_id"] == nil {
		t.Fatalf("exchanged session view: %s", view.Body)
	}
	linkURL := s.linkURL("w", "")
	b := s.browser()
	b.get(linkURL)
	b.post("/link/cancel", map[string][]string{})
	if b.result == nil || b.result.Query().Get("error") != "access_denied" || b.result.Query().Get("link_session_id") == "" {
		t.Fatalf("cancel redirect: %v", b.result)
	}
	if code := b.get(linkURL); code != 409 && b.result == nil {
		t.Fatalf("reused link token: %d", code)
	}
}

func TestGarbageCursorIsRejected(t *testing.T) {
	s := newStack(t)
	conn := s.connect("tunde", "bank-a")
	id := fmt.Sprint(conn.Accounts[0]["id"])
	r := s.call("GET", "/v1/accounts/"+id+"/transactions/sync?cursor=cur_garbage", nil, withToken(conn.AccessToken))
	if r.Status != 400 || r.Code() != "invalid_cursor" {
		t.Fatalf("garbage cursor: %d %s", r.Status, r.Body)
	}
}

func TestRevokeAndReconnectKeepsIDs(t *testing.T) {
	if testingExternal() {
		t.Skip("needs accounts no other connection covers")
	}
	s := newStack(t)
	conn := s.connect("adaeze", "bank-a")
	id := fmt.Sprint(accountAt(t, conn, "current")["id"])
	before, _ := s.syncAll(conn.AccessToken, id, "", 500)
	s.ctl("POST", "/connections/"+conn.ConnectionID+"/revoke", nil)
	c := s.must(s.call("GET", "/v1/connection", nil, withToken(conn.AccessToken)), 200)
	if c.JSON["status"] != "revoked" {
		t.Fatalf("status after revoke: %v", c.JSON["status"])
	}
	r := s.call("GET", "/v1/accounts/"+id+"/transactions/sync", nil, withToken(conn.AccessToken))
	if r.Status != 409 || r.Code() != "connection_revoked" {
		t.Fatalf("sync after revoke: %d %s", r.Status, r.Body)
	}
	r = s.call("POST", "/v1/connection/reauth", nil, withToken(conn.AccessToken))
	if r.Status != 409 || r.Code() != "connection_revoked" {
		t.Fatalf("reauth after revoke: %d %s", r.Status, r.Body)
	}
	if len(s.eventsOf("connection.status_changed", func(d map[string]any) bool {
		return d["connection_id"] == conn.ConnectionID && d["status"] == "revoked"
	})) == 0 {
		t.Fatal("no revoked webhook")
	}
	again := s.connect("adaeze", "bank-a")
	if again.ConnectionID == conn.ConnectionID {
		t.Fatal("reconnect reused the connection ID")
	}
	id2 := fmt.Sprint(accountAt(t, again, "current")["id"])
	if id2 != id {
		t.Fatalf("account ID changed across consents: %s vs %s", id, id2)
	}
	after, _ := s.syncAll(again.AccessToken, id2, before.cursor, 500)
	for lid, l := range after.lines {
		if _, ok := before.lines[lid]; !ok && l["status"] == "posted" {
			bt, _ := time.Parse(time.RFC3339, fmt.Sprint(l["booked_at"]))
			if clock.DateOf(bt).Before(s.today()) {
				t.Fatalf("an old line came back with a new ID: %v", l)
			}
		}
	}
	d := s.call("POST", "/v1/connections/remove", nil, withToken(again.AccessToken))
	s.must(d, 204)
	if r := s.call("POST", "/v1/connections/remove", nil, withToken(again.AccessToken)); r.Status != 401 || r.Code() != "invalid_access_token" {
		t.Fatalf("remove again: %d %s", r.Status, r.Body)
	}
}

func TestReauthKeepsTheConnection(t *testing.T) {
	s := newStack(t)
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
	ex := s.must(s.call("POST", "/v1/public_token/exchange", map[string]any{"public_token": res.PublicToken}), 200)
	if ex.JSON["connection_id"] != conn.ConnectionID {
		t.Fatal("reauth created a new connection")
	}
	s.syncNow()
	c := s.must(s.call("GET", "/v1/connection", nil, withToken(conn.AccessToken)), 200)
	if c.JSON["status"] != "active" {
		t.Fatalf("status after reauth: %v", c.JSON["status"])
	}
	accts := s.must(s.call("GET", "/v1/accounts", nil, withToken(conn.AccessToken)), 200)
	if fmt.Sprint(accts.JSON["data"].([]any)[0].(map[string]any)["id"]) != id {
		t.Fatal("account ID changed after reauth")
	}
	s.must(s.call("GET", "/v1/accounts/"+id+"/transactions/sync?limit=5", nil, withToken(conn.AccessToken)), 200)
}

func TestRefreshAndRateLimit(t *testing.T) {
	s := newStack(t)
	conn := s.connect("ngozi", "bank-b")
	id := fmt.Sprint(conn.Accounts[0]["id"])
	r := s.must(s.call("POST", "/v1/accounts/"+id+"/refresh", nil, withToken(conn.AccessToken)), 202)
	ref := fmt.Sprint(r.JSON["id"])
	r = s.call("POST", "/v1/accounts/"+id+"/refresh", nil, withToken(conn.AccessToken))
	if r.Status != 429 || r.Code() != "rate_limited" || r.Header.Get("Retry-After") == "" {
		t.Fatalf("second refresh: %d %s", r.Status, r.Body)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(s.eventsOf("refresh.completed", func(d map[string]any) bool { return d["refresh_id"] == ref && d["outcome"] == "succeeded" })) > 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("no refresh.completed webhook")
}

func TestBankOutage(t *testing.T) {
	s := newStack(t)
	conn := s.connect("chidi", "bank-b")
	s.ctl("POST", "/faults", map[string]any{"kind": "down", "bank": "bank-b"})
	var c Resp
	waitFor(t, 15*time.Second, "the connection to be institution_down during the outage", func() bool {
		s.syncNow()
		c = s.must(s.call("GET", "/v1/connection", nil, withToken(conn.AccessToken)), 200)
		return c.JSON["status"] == "institution_down"
	})
	r := s.call("POST", "/v1/connections/remove", nil, withToken(conn.AccessToken))
	if r.Status != 503 || r.Code() != "institution_down" {
		t.Fatalf("delete during outage: %d %s", r.Status, r.Body)
	}
	tunde := s.persona("tunde")
	before := s.balances(tunde)
	if _, err := s.payTry("bank-a", tunde.Accounts[0], "bank-b", s.persona("chidi").Accounts[0], "1000", nil); err == nil || !strings.Contains(err.Error(), "bank_unavailable") {
		t.Fatalf("transfer to a bank that is down: %v", err)
	}
	if after := s.balances(tunde); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("a refused transfer moved money: %v then %v", before, after)
	}
	b := s.browser()
	b.get(s.linkURL("w", ""))
	b.post("/link/institution", map[string][]string{"institution": {"bank-b"}})
	if b.result == nil || b.result.Query().Get("error") != "institution_down" {
		t.Fatalf("widget during outage: %v", b.result)
	}
	s.ctl("DELETE", "/faults?bank=bank-b", nil)
	waitFor(t, 15*time.Second, "the connection to be active after the outage", func() bool {
		s.syncNow()
		c = s.must(s.call("GET", "/v1/connection", nil, withToken(conn.AccessToken)), 200)
		return c.JSON["status"] == "active"
	})
}

func TestOwnAccountTransferMatchesBySessionID(t *testing.T) {
	s := newStack(t)
	a := s.connect("adaeze", "bank-a")
	b := s.connect("adaeze", "bank-b")
	pa, pb := s.personaAt("adaeze", "bank-a"), s.personaAt("adaeze", "bank-b")
	if pa.BVN != pb.BVN {
		t.Fatalf("one customer has BVNs %s and %s", pa.BVN, pb.BVN)
	}
	s.pay("bank-a", pa.Accounts[0], "bank-b", pb.Accounts[0], "100000")
	s.syncNow()
	fa, _ := s.syncAll(a.AccessToken, fmt.Sprint(accountAt(t, a, "current")["id"]), "", 500)
	fb, _ := s.syncAll(b.AccessToken, fmt.Sprint(b.Accounts[0]["id"]), "", 500)
	sessions := map[string]int64{}
	for _, l := range fa.lines {
		if l["channel"] == "nip" && num(l["amount_minor"]) == -10000000 {
			sessions[fmt.Sprint(l["references"].(map[string]any)["session_id"])] = num(l["amount_minor"])
		}
	}
	matched := false
	for _, l := range fb.lines {
		sid := fmt.Sprint(l["references"].(map[string]any)["session_id"])
		if amt, ok := sessions[sid]; ok && amt == -num(l["amount_minor"]) {
			matched = true
		}
	}
	if !matched {
		t.Fatal("no Bank B credit matched the Bank A debit by session ID")
	}
}
