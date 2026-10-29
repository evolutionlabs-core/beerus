package contracttest

import (
	"bytes"
	"crypto/tls"
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
	r := s.call("GET", "/v1/webhook_keys", nil, apiKey(""), skipValidation())
	if r.Status != 401 || r.Code() != "unauthenticated" {
		t.Fatalf("missing key: %d %s", r.Status, r.Body)
	}
	r = s.call("GET", "/v1/webhook_keys", nil, apiKey("bk_test_wrong"), skipValidation())
	if r.Status != 401 || r.Code() != "unauthenticated" {
		t.Fatalf("wrong key: %d %s", r.Status, r.Body)
	}
	noCert := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: s.rootPool()}}, Timeout: 5 * time.Second}
	req, _ := http.NewRequest("GET", s.cfg.PartnerURL()+"/v1/webhook_keys", nil)
	req.Header.Set("X-Api-Key", s.apiKey)
	if resp, err := noCert.Do(req); err == nil {
		resp.Body.Close()
		t.Fatalf("a request without a client certificate was accepted: %d", resp.StatusCode)
	}
	s.must(s.call("GET", "/v1/webhook_keys", nil), 200)
	r = s.call("GET", "/v1/nothing", nil, skipValidation())
	if r.Status != 404 || r.Code() != "not_found" {
		t.Fatalf("unknown path: %d %s", r.Status, r.Body)
	}
}

func TestLinkSessionValidation(t *testing.T) {
	s := newStack(t)
	today := s.today()
	cases := []map[string]any{
		{"start_date": "2026-13-01", "workspace_id": "w", "redirect_uri": s.redirect},
		{"start_date": today.String(), "workspace_id": "", "redirect_uri": s.redirect},
		{"start_date": today.String(), "workspace_id": "w", "redirect_uri": s.redirect + "/extra"},
		{"start_date": today.AddDays(5).String(), "workspace_id": "w", "redirect_uri": s.redirect},
	}
	for i, body := range cases {
		r := s.call("POST", "/v1/link_sessions", body)
		if r.Status != 400 || r.Code() != "invalid_request" {
			t.Errorf("case %d: %d %s", i, r.Status, r.Body)
		}
	}
	r := s.call("POST", "/v1/link_sessions", map[string]any{"start_date": today.String(), "workspace_id": "w", "redirect_uri": s.redirect}, noIdem())
	if r.Status != 400 {
		t.Fatalf("missing Idempotency-Key: %d", r.Status)
	}
	ls := s.must(s.call("POST", "/v1/link_sessions", map[string]any{"start_date": today.String(), "workspace_id": "w", "redirect_uri": s.redirect}), 201)
	if !strings.HasPrefix(fmt.Sprint(ls.JSON["widget_url"]), s.cfg.WidgetURL()+"/link?token=") {
		t.Fatalf("widget_url: %v", ls.JSON["widget_url"])
	}
	got := s.must(s.call("GET", "/v1/link_sessions/"+fmt.Sprint(ls.JSON["id"]), nil), 200)
	if _, leaked := got.JSON["link_token"]; leaked || got.JSON["status"] != "created" {
		t.Fatalf("link session view: %s", got.Body)
	}
}

func TestIdempotency(t *testing.T) {
	s := newStack(t)
	body := map[string]any{"start_date": s.today().AddDays(-30).String(), "workspace_id": "ws_idem", "redirect_uri": s.redirect}
	key := randomKey()
	a := s.must(s.call("POST", "/v1/link_sessions", body, withKey(key)), 201)
	b := s.must(s.call("POST", "/v1/link_sessions", body, withKey(key)), 201)
	if !bytes.Equal(a.Body, b.Body) {
		t.Fatalf("replay differs:\n%s\n%s", a.Body, b.Body)
	}
	if b.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatal("replay not marked")
	}
	body["workspace_id"] = "ws_other"
	r := s.call("POST", "/v1/link_sessions", body, withKey(key))
	if r.Status != 422 || r.Code() != "idempotency_key_reused" {
		t.Fatalf("reused key: %d %s", r.Status, r.Body)
	}
	c := s.must(s.call("POST", "/v1/link_sessions", body), 201)
	if c.JSON["id"] == a.JSON["id"] {
		t.Fatal("a new key returned the old session")
	}
}

func TestConnectAndSync(t *testing.T) {
	s := newStack(t)
	today := s.today()
	conn := s.connect("adaeze", "bank-a", today.AddDays(-60))
	if len(conn.Accounts) != 2 {
		t.Fatalf("want 2 Bank A accounts, got %d", len(conn.Accounts))
	}
	acct := accountAt(t, conn, "current")
	id := fmt.Sprint(acct["id"])
	small, _ := s.syncAll(id, "", 7)
	big, last := s.syncAll(id, "", 500)
	if len(big.lines) < 30 {
		t.Fatalf("only %d lines in 60 days", len(big.lines))
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
	again, r := s.syncMore(id, big, 100)
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
	open := s.must(s.call("GET", fmt.Sprintf("/v1/accounts/%s/balance?as_of=%s", id, start.AddDays(-1)), nil), 200)
	opening := num(open.JSON["ledger_balance"].(map[string]any)["amount_minor"])
	run := opening
	for _, l := range posted {
		run += num(l["amount_minor"])
		if num(l["balance_after_minor"]) != run {
			t.Fatalf("balance_after chain broken at %v", l["id"])
		}
	}
	got := s.must(s.call("GET", "/v1/accounts/"+id, nil), 200)
	current := num(got.JSON["balances"].(map[string]any)["current"].(map[string]any)["amount_minor"])
	if opening+sum != current {
		t.Fatalf("opening %d + posted %d != current %d", opening, sum, current)
	}
	yesterday := s.must(s.call("GET", fmt.Sprintf("/v1/accounts/%s/balance?as_of=%s", id, today.AddDays(-1)), nil), 200)
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
			t.Errorf("no %q line in 60 days", want)
		}
	}

	if s.sink != nil {
		pub := s.publicKey()
		hooks := s.sink.all()
		seen := map[string]bool{}
		for _, h := range hooks {
			if err := verifyV1a(pub, h); err != nil {
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

func TestBalanceRangeAndHistoryLimit(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an account no earlier consent covered")
	}
	s := newStack(t)
	today := s.today()
	conn := s.connect("ngozi", "bank-b", today.AddDays(-365))
	id := fmt.Sprint(conn.Accounts[0]["id"])
	c := s.must(s.call("GET", "/v1/connections/"+conn.ConnectionID, nil), 200)
	eff := fmt.Sprint(c.JSON["effective_start_date"])
	if eff != today.AddDays(-180).String() {
		t.Fatalf("effective_start_date %s, want %s", eff, today.AddDays(-180))
	}
	effDate, _ := clock.ParseDate(eff)
	ok := s.must(s.call("GET", fmt.Sprintf("/v1/accounts/%s/balance?as_of=%s", id, effDate.AddDays(-1)), nil), 200)
	if ok.JSON["effective_start_date"] != eff {
		t.Fatalf("balance effective_start_date: %s", ok.Body)
	}
	for _, d := range []clock.Date{effDate.AddDays(-2), today} {
		r := s.call("GET", fmt.Sprintf("/v1/accounts/%s/balance?as_of=%s", id, d), nil)
		if r.Status != 422 || r.Code() != "as_of_out_of_range" {
			t.Errorf("as_of %s: %d %s", d, r.Status, r.Body)
		}
	}
	r := s.call("GET", "/v1/accounts/"+id+"/balance?as_of=yesterday", nil)
	if r.Status != 400 {
		t.Errorf("bad as_of: %d", r.Status)
	}
	if s.sink != nil && len(s.eventsOf("historical_sync.completed", func(d map[string]any) bool { return d["effective_start_date"] == eff })) == 0 {
		t.Error("historical_sync.completed did not carry effective_start_date")
	}
}

func TestExchangeErrors(t *testing.T) {
	s := newStack(t)
	start := s.today().AddDays(-10)
	mk := func() (string, linkResult) {
		ls := s.must(s.call("POST", "/v1/link_sessions", map[string]any{"start_date": start.String(), "workspace_id": "w", "redirect_uri": s.redirect}), 201)
		return fmt.Sprint(ls.JSON["id"]), s.completeLink(fmt.Sprint(ls.JSON["widget_url"]), "bank-a", "tunde", nil)
	}
	id1, r1 := mk()
	id2, r2 := mk()
	r := s.call("POST", "/v1/connections/exchange", map[string]any{"code": r1.Code, "link_session_id": id2})
	if r.Status != 403 || r.Code() != "link_session_mismatch" {
		t.Fatalf("crossed code: %d %s", r.Status, r.Body)
	}
	r = s.call("POST", "/v1/connections/exchange", map[string]any{"code": "oc_nope", "link_session_id": id1})
	if r.Status != 404 || r.Code() != "invalid_code" {
		t.Fatalf("unknown code: %d %s", r.Status, r.Body)
	}
	s.must(s.call("POST", "/v1/connections/exchange", map[string]any{"code": r1.Code, "link_session_id": id1}), 201)
	r = s.call("POST", "/v1/connections/exchange", map[string]any{"code": r1.Code, "link_session_id": id1})
	if r.Status != 404 || r.Code() != "invalid_code" {
		t.Fatalf("reused code: %d %s", r.Status, r.Body)
	}
	view := s.must(s.call("GET", "/v1/link_sessions/"+id1, nil), 200)
	if view.JSON["status"] != "exchanged" || view.JSON["connection_id"] == nil {
		t.Fatalf("exchanged session view: %s", view.Body)
	}
	_ = r2
	ls := s.must(s.call("POST", "/v1/link_sessions", map[string]any{"start_date": start.String(), "workspace_id": "w", "redirect_uri": s.redirect}), 201)
	b := s.browser()
	b.get(fmt.Sprint(ls.JSON["widget_url"]))
	b.post("/link/cancel", map[string][]string{})
	if b.result == nil || b.result.Query().Get("error") != "access_denied" || b.result.Query().Get("link_session_id") != ls.JSON["id"] {
		t.Fatalf("cancel redirect: %v", b.result)
	}
	if code := b.get(fmt.Sprint(ls.JSON["widget_url"])); code != 409 && b.result == nil {
		t.Fatalf("reused link token: %d", code)
	}
}

func TestPendingAndModifiedLines(t *testing.T) {
	s := newStack(t)
	conn := s.connect("adaeze", "bank-b", s.today().AddDays(-20))
	id := fmt.Sprint(conn.Accounts[0]["id"])
	st, _ := s.syncAll(id, "", 100)
	s.ctl("POST", "/scenarios/pending_amount_change", map[string]any{"account_id": id})
	s.syncNow()
	st, _ = s.syncMore(id, st, 100)
	var pending map[string]any
	for _, l := range st.lines {
		if l["status"] == "pending" && num(l["amount_minor"]) == -1450000 {
			pending = l
		}
	}
	if pending == nil {
		t.Fatal("the pending line did not arrive")
	}
	s.advance("4h")
	st, _ = s.syncMore(id, st, 100)
	if !st.removed[fmt.Sprint(pending["id"])] {
		t.Fatal("the pending line was not removed after it posted")
	}
	var postedLine map[string]any
	for _, l := range st.lines {
		if l["pending_transaction_id"] == pending["id"] {
			postedLine = l
		}
	}
	if postedLine == nil || num(postedLine["amount_minor"]) != -1500000 || postedLine["status"] != "posted" {
		t.Fatalf("posted version: %v", postedLine)
	}
	ids := map[string]bool{}
	for id := range st.lines {
		ids[id] = true
	}
	s.ctl("POST", "/scenarios/renarration", map[string]any{"account_id": id, "count": 2})
	s.syncNow()
	before := len(st.modified)
	st, _ = s.syncMore(id, st, 100)
	if len(st.modified)-before != 2 {
		t.Fatalf("want 2 modified lines, got %d", len(st.modified)-before)
	}
	for _, m := range st.modified[before:] {
		if m["modification_reason"] != "description_changed" || m["status"] != "posted" {
			t.Fatalf("modified line: %v", m)
		}
	}
	s.ctl("POST", "/scenarios/unstable_ids", map[string]any{"institution": "bank-b"})
	s.advance("26h")
	before = len(st.modified)
	st, _ = s.syncMore(id, st, 100)
	refChanged := 0
	for _, m := range st.modified[before:] {
		if m["modification_reason"] == "bank_reference_changed" {
			refChanged++
		}
	}
	if refChanged == 0 {
		t.Fatal("unstable_ids produced no bank_reference_changed lines")
	}
	for id := range ids {
		if _, ok := st.lines[id]; !ok && !st.removed[id] {
			t.Fatalf("line %s vanished under unstable_ids", id)
		}
	}
}

func TestSnapshotRestoreResetsCursor(t *testing.T) {
	s := newStack(t)
	conn := s.connect("tunde", "bank-a", s.today().AddDays(-30))
	id := fmt.Sprint(conn.Accounts[0]["id"])
	snap := s.ctl("POST", "/snapshots", map[string]string{"name": "before"})
	first, _ := s.syncAll(id, "", 100)
	s.advance("48h")
	s.ctl("POST", "/scenarios/fee_vat_levy_lines", map[string]any{"account_id": id})
	s.advance("1m")
	first, _ = s.syncMore(id, first, 100)
	restored := s.ctl("POST", "/snapshots/"+fmt.Sprint(snap["id"])+"/restore", nil)
	if restored["world_id"] == first.worldID {
		t.Fatal("restore kept the world ID")
	}
	r := s.call("GET", "/v1/accounts/"+id+"/transactions/sync?cursor="+first.cursor, nil)
	if r.Status != 409 || r.Code() != "cursor_reset" {
		t.Fatalf("old cursor after restore: %d %s", r.Status, r.Body)
	}
	s.syncNow()
	again, _ := s.syncAll(id, "", 100)
	if again.worldID == first.worldID {
		t.Fatal("world_id did not change")
	}
	for lid := range again.lines {
		if _, ok := first.lines[lid]; !ok && !first.removed[lid] {
			t.Fatalf("line %s is new after the restore", lid)
		}
	}
	if len(again.lines) >= len(first.lines) {
		t.Fatalf("the restored world should hold fewer lines: %d vs %d", len(again.lines), len(first.lines))
	}
	r = s.call("GET", "/v1/accounts/"+id+"/transactions/sync?cursor=cur_garbage", nil)
	if r.Status != 400 || r.Code() != "invalid_cursor" {
		t.Fatalf("garbage cursor: %d %s", r.Status, r.Body)
	}
}

func TestRevokeAndReconnectKeepsIDs(t *testing.T) {
	if testingExternal() {
		t.Skip("needs accounts no other connection covers")
	}
	s := newStack(t)
	start := s.today().AddDays(-30)
	conn := s.connect("adaeze", "bank-a", start)
	id := fmt.Sprint(accountAt(t, conn, "current")["id"])
	before, _ := s.syncAll(id, "", 500)
	s.ctl("POST", "/connections/"+conn.ConnectionID+"/revoke", nil)
	c := s.must(s.call("GET", "/v1/connections/"+conn.ConnectionID, nil), 200)
	if c.JSON["status"] != "revoked" {
		t.Fatalf("status after revoke: %v", c.JSON["status"])
	}
	r := s.call("GET", "/v1/accounts/"+id+"/transactions/sync", nil)
	if r.Status != 409 || r.Code() != "connection_revoked" {
		t.Fatalf("sync after revoke: %d %s", r.Status, r.Body)
	}
	r = s.call("POST", "/v1/connections/"+conn.ConnectionID+"/reauth", nil)
	if r.Status != 409 || r.Code() != "connection_revoked" {
		t.Fatalf("reauth after revoke: %d %s", r.Status, r.Body)
	}
	if len(s.eventsOf("connection.status_changed", func(d map[string]any) bool {
		return d["connection_id"] == conn.ConnectionID && d["status"] == "revoked"
	})) == 0 {
		t.Fatal("no revoked webhook")
	}
	again := s.connect("adaeze", "bank-a", start)
	if again.ConnectionID == conn.ConnectionID {
		t.Fatal("reconnect reused the connection ID")
	}
	id2 := fmt.Sprint(accountAt(t, again, "current")["id"])
	if id2 != id {
		t.Fatalf("account ID changed across consents: %s vs %s", id, id2)
	}
	after, _ := s.syncAll(id2, before.cursor, 500)
	for lid, l := range after.lines {
		if _, ok := before.lines[lid]; !ok && l["status"] == "posted" {
			bt, _ := time.Parse(time.RFC3339, fmt.Sprint(l["booked_at"]))
			if clock.DateOf(bt).Before(s.today()) {
				t.Fatalf("an old line came back with a new ID: %v", l)
			}
		}
	}
	d := s.call("DELETE", "/v1/connections/"+again.ConnectionID, nil)
	s.must(d, 204)
	s.must(s.call("DELETE", "/v1/connections/"+again.ConnectionID, nil), 204)
	c = s.must(s.call("GET", "/v1/connections/"+again.ConnectionID, nil), 200)
	if c.JSON["status"] != "revoked" {
		t.Fatalf("status after delete: %v", c.JSON["status"])
	}
}

func TestConsentExpiryAndReauth(t *testing.T) {
	s := newStack(t)
	conn := s.connect("tunde", "bank-a", s.today().AddDays(-30))
	id := fmt.Sprint(conn.Accounts[0]["id"])
	s.advance("366d")
	c := s.must(s.call("GET", "/v1/connections/"+conn.ConnectionID, nil), 200)
	if c.JSON["status"] != "expired" {
		t.Fatalf("status after a year: %v", c.JSON["status"])
	}
	r := s.call("GET", "/v1/accounts/"+id+"/transactions/sync", nil)
	if r.Status != 409 || r.Code() != "reauth_required" {
		t.Fatalf("sync after expiry: %d %s", r.Status, r.Body)
	}
	ls := s.must(s.call("POST", "/v1/connections/"+conn.ConnectionID+"/reauth", map[string]string{"state": "again"}), 201)
	if ls.JSON["connection_id"] != conn.ConnectionID {
		t.Fatalf("reauth session: %s", ls.Body)
	}
	res := s.completeLink(fmt.Sprint(ls.JSON["widget_url"]), "", "tunde", nil)
	if res.Code == "" || res.State != "again" {
		t.Fatalf("reauth link: %+v", res)
	}
	ex := s.must(s.call("POST", "/v1/connections/exchange", map[string]any{"code": res.Code, "link_session_id": res.SessionID}), 200)
	if ex.JSON["id"] != conn.ConnectionID {
		t.Fatal("reauth created a new connection")
	}
	s.syncNow()
	c = s.must(s.call("GET", "/v1/connections/"+conn.ConnectionID, nil), 200)
	if c.JSON["status"] != "active" {
		t.Fatalf("status after reauth: %v", c.JSON["status"])
	}
	accts := s.must(s.call("GET", "/v1/connections/"+conn.ConnectionID+"/accounts", nil), 200)
	if fmt.Sprint(accts.JSON["data"].([]any)[0].(map[string]any)["id"]) != id {
		t.Fatal("account ID changed after reauth")
	}
	s.must(s.call("GET", "/v1/accounts/"+id+"/transactions/sync?limit=5", nil), 200)
}

func TestRefreshAndRateLimit(t *testing.T) {
	s := newStack(t)
	conn := s.connect("ngozi", "bank-b", s.today().AddDays(-10))
	id := fmt.Sprint(conn.Accounts[0]["id"])
	r := s.must(s.call("POST", "/v1/accounts/"+id+"/refresh", nil), 202)
	ref := fmt.Sprint(r.JSON["id"])
	r = s.call("POST", "/v1/accounts/"+id+"/refresh", nil)
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
	conn := s.connect("chidi", "bank-b", s.today().AddDays(-10))
	s.ctl("POST", "/scenarios/bank_outage", map[string]any{"institution": "bank-b", "duration": "2h"})
	s.syncNow()
	c := s.must(s.call("GET", "/v1/connections/"+conn.ConnectionID, nil), 200)
	if c.JSON["status"] != "institution_down" {
		t.Fatalf("status during outage: %v", c.JSON["status"])
	}
	r := s.call("DELETE", "/v1/connections/"+conn.ConnectionID, nil)
	if r.Status != 503 || r.Code() != "institution_down" {
		t.Fatalf("delete during outage: %d %s", r.Status, r.Body)
	}
	ls := s.must(s.call("POST", "/v1/link_sessions", map[string]any{"start_date": s.today().String(), "workspace_id": "w", "redirect_uri": s.redirect}), 201)
	b := s.browser()
	b.get(fmt.Sprint(ls.JSON["widget_url"]))
	b.post("/link/institution", map[string][]string{"institution": {"bank-b"}})
	if b.result == nil || b.result.Query().Get("error") != "institution_down" {
		t.Fatalf("widget during outage: %v", b.result)
	}
	s.advance("3h")
	c = s.must(s.call("GET", "/v1/connections/"+conn.ConnectionID, nil), 200)
	if c.JSON["status"] != "active" {
		t.Fatalf("status after outage: %v", c.JSON["status"])
	}
}

func TestOwnAccountTransferMatchesBySessionID(t *testing.T) {
	s := newStack(t)
	start := s.today().AddDays(-5)
	a := s.connect("adaeze", "bank-a", start)
	b := s.connect("adaeze", "bank-b", start)
	s.ctl("POST", "/scenarios/own_account_transfer_across_banks", map[string]any{"persona": "adaeze"})
	s.advance("1m")
	fa, _ := s.syncAll(fmt.Sprint(accountAt(t, a, "current")["id"]), "", 500)
	fb, _ := s.syncAll(fmt.Sprint(b.Accounts[0]["id"]), "", 500)
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

func TestCustomerPageRevokes(t *testing.T) {
	s := newStack(t)
	conn := s.connect("chidi", "bank-b", s.today().AddDays(-5))
	p := s.persona("chidi")
	b := s.browser()
	b.get(s.cfg.WidgetURL() + "/customer")
	b.post("/customer/login", map[string][]string{"username": {p.Username}, "password": {p.Password}})
	if !strings.Contains(b.last, conn.ConnectionID) {
		t.Fatalf("customer page does not list the connection: %.300s", b.last)
	}
	b.post("/customer/revoke", map[string][]string{"connection": {conn.ConnectionID}})
	c := s.must(s.call("GET", "/v1/connections/"+conn.ConnectionID, nil), 200)
	if c.JSON["status"] != "revoked" {
		t.Fatalf("status after customer revoke: %v", c.JSON["status"])
	}
}
