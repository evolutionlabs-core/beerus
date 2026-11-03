package contracttest

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func postedKeys(t *testing.T, st *feedState) map[string]string {
	t.Helper()
	seen := map[string]string{}
	for id, l := range st.lines {
		if l["status"] != "posted" {
			continue
		}
		key := fmt.Sprint(l["value_date"], "|", l["amount_minor"], "|", l["description"], "|", l["balance_after_minor"])
		if other, dup := seen[key]; dup {
			t.Fatalf("one transaction appears twice: %s and %s (%s)", other, id, key)
		}
		seen[key] = id
	}
	return seen
}

func idSet(st *feedState) []string {
	ids := []string{}
	for id := range st.lines {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func TestDoneStory(t *testing.T) {
	s := newBankStack(t)
	cur := ""
	step := func(n int, what string) {
		cur = fmt.Sprintf("step %d (%s)", n, what)
		t.Log(cur)
	}
	check := func(ok bool, format string, args ...any) {
		t.Helper()
		if !ok {
			t.Fatalf(cur+": "+format, args...)
		}
	}
	today := s.today()
	start := today.AddDays(-365)

	step(1, "Adaeze's Bakery banks with Bank A and Bank B")
	byBank := map[string][]map[string]any{}
	var adaeze map[string]any
	for _, p := range s.ctl("GET", "/personas", nil)["data"].([]any) {
		if m := p.(map[string]any); m["id"] == "adaeze" {
			adaeze = m
		}
	}
	check(adaeze != nil && adaeze["name"] == "Adaeze's Bakery Ltd", "persona: %v", adaeze)
	for _, a := range adaeze["accounts"].([]any) {
		m := a.(map[string]any)
		byBank[fmt.Sprint(m["institution"])] = append(byBank[fmt.Sprint(m["institution"])], m)
	}
	check(len(byBank["bank-a"]) > 0 && len(byBank["bank-b"]) > 0, "accounts: %v", byBank)
	var bankACurrent map[string]any
	for _, a := range byBank["bank-a"] {
		if a["type"] == "current" {
			bankACurrent = a
		}
	}
	check(bankACurrent != nil, "no current account at Bank A")

	step(2, "Connect bank, pick Bank A, redirect to the bank")
	ls := s.must(s.call("POST", "/v1/link_sessions", map[string]any{"start_date": start.String(), "workspace_id": "ws_adaeze_bakery", "redirect_uri": s.redirect, "state": "st-done"}), 201)
	b := s.browser()
	b.get(fmt.Sprint(ls.JSON["widget_url"]))
	check(strings.Contains(b.last, "Bank A") && strings.Contains(b.last, "Bank B"), "the widget does not offer both banks: %.300s", b.last)
	b.post("/link/institution", url.Values{"institution": {"bank-a"}})
	check(b.result == nil && b.atBank(), "the widget did not send her to the bank: %v %.200s", b.cur, b.last)

	step(3, "sign in, 2FA code, CBN consent screen, pick accounts, approve")
	base := b.cur.Scheme + "://" + b.cur.Host
	p := s.persona("adaeze")
	b.postTo(base, "/login", url.Values{"username": {p.Username}, "password": {p.Password}})
	otp := reOTP.FindStringSubmatch(b.last)
	check(otp != nil, "no 2FA code in the inbox: %.300s", b.last)
	b.postTo(base, "/verify", url.Values{"otp": {otp[1]}})
	for _, want := range []string{s.cfg.Connect.LegalName, s.cfg.Connect.RegistryID, s.cfg.Connect.CACNumber, "Evolution Labs", "to keep your books in sync", "Read only", "Anonymised analytics", "12 months", "Connected apps"} {
		check(strings.Contains(b.last, want), "the consent screen is missing %q", want)
	}
	check(strings.Contains(b.last, "shares history from"), "the consent screen does not say the bank limits history")
	offered := []string{}
	for _, m := range reAccount.FindAllStringSubmatch(b.last, -1) {
		offered = append(offered, m[1])
	}
	check(len(offered) == len(byBank["bank-a"]), "the bank offered %d accounts, want %d", len(offered), len(byBank["bank-a"]))
	b.postTo(base, "/authorize/consent", url.Values{"account": {fmt.Sprint(bankACurrent["nuban"])}, "action": {"approve"}})
	check(b.result != nil && b.result.Query().Get("code") != "", "no code on the redirect to Balance: %.300s", b.last)
	q := b.result.Query()
	check(q.Get("state") == "st-done" && q.Get("link_session_id") == ls.JSON["id"], "redirect: %v", b.result)
	conn := s.must(s.call("POST", "/v1/connections/exchange", map[string]any{"code": q.Get("code"), "link_session_id": q.Get("link_session_id")}), 201)
	connA := fmt.Sprint(conn.JSON["id"])
	s.syncNow()
	check(s.must(s.call("GET", "/v1/connections/"+connA, nil), 200).JSON["status"] == "active", "Bank A connection not active")
	accts := s.must(s.call("GET", "/v1/connections/"+connA+"/accounts", nil), 200).JSON["data"].([]any)
	check(len(accts) == 1 && accts[0].(map[string]any)["type"] == "current", "only the picked account should be shared: %v", accts)
	idA := fmt.Sprint(accts[0].(map[string]any)["id"])

	step(4, "opening balance and history up to the bank's limit")
	feedA, _ := s.syncAll(idA, "", 500)
	eff := s.firstDate(idA, start)
	check(eff.After(start), "history was not limited: effective start %s, asked %s", eff, start)
	opening := num(s.must(s.call("GET", fmt.Sprintf("/v1/accounts/%s/balance?as_of=%s", idA, eff.AddDays(-1)), nil), 200).JSON["ledger_balance"].(map[string]any)["amount_minor"])
	posted := []map[string]any{}
	kinds, reversal := map[string]bool{}, false
	var sum int64
	for _, l := range feedA.lines {
		kinds[fmt.Sprint(l["description"])] = true
		if strings.HasPrefix(fmt.Sprint(l["description"]), "Reversal") || l["channel"] == "reversal" {
			reversal = true
		}
		if l["status"] == "posted" {
			posted = append(posted, l)
			sum += num(l["amount_minor"])
		}
	}
	sort.Slice(posted, func(i, j int) bool { return num(posted[i]["sequence"]) < num(posted[j]["sequence"]) })
	run := opening
	for _, l := range posted {
		run += num(l["amount_minor"])
		check(num(l["balance_after_minor"]) == run, "balance chain broken at %v", l["id"])
	}
	current := num(s.must(s.call("GET", "/v1/accounts/"+idA, nil), 200).JSON["balances"].(map[string]any)["current"].(map[string]any)["amount_minor"])
	check(opening+sum == current, "opening %d + history %d != current %d", opening, sum, current)
	for _, want := range []string{"Transfer fee", "VAT on transfer fee", "Stamp duty"} {
		check(kinds[want], "no %q line in the history", want)
	}
	check(reversal, "no reversal in the history")
	if s.app != nil {
		checkSettled(t, s.app)
	}

	b2 := s.connect("adaeze", "bank-b", start)
	idB := fmt.Sprint(b2.Accounts[0]["id"])
	connB := b2.ConnectionID
	feedB, _ := s.syncAll(idB, "", 500)
	check(len(feedB.lines) > 0, "no history from Bank B")

	step(5, "a customer at Bank B pays her through the NIP switch")
	var chidiB string
	for _, pp := range s.ctl("GET", "/personas", nil)["data"].([]any) {
		if m := pp.(map[string]any); m["id"] == "chidi" {
			for _, a := range m["accounts"].([]any) {
				if am := a.(map[string]any); am["institution"] == "bank-b" {
					chidiB = fmt.Sprint(am["key"])
				}
			}
		}
	}
	check(chidiB != "", "no Bank B account for the customer")
	before := s.ctl("GET", "/nip/settlement", nil)
	delivered := func(typ string, match func(map[string]any) bool) int {
		if s.sink == nil {
			return len(s.eventsOf(typ, match))
		}
		pub := s.publicKey()
		n := 0
		for _, h := range s.sink.all() {
			var ev struct {
				Type string         `json:"type"`
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal(h.Body, &ev); err != nil || ev.Type != typ || !match(ev.Data) {
				continue
			}
			check(verifyV1a(pub, h) == nil, "webhook signature")
			n++
		}
		return n
	}
	isA := func(d map[string]any) bool { return d["account_id"] == idA }
	hooksBefore := delivered("sync.available", isA)
	pay, err := s.ctlTry("POST", "/transfers", map[string]any{"from": chidiB, "to": fmt.Sprint(bankACurrent["key"]), "amount": "2500", "narration": "Cake order"})
	check(err == nil, "payment: %v", err)
	sid := fmt.Sprint(pay["session_id"])
	check(len(sid) == 30 && pay["response_code"] == "00" && strings.HasPrefix(sid, "999002"), "payment reply: %v", pay)
	s.syncNow()
	feedA, _ = s.syncMore(idA, feedA, 500)
	findBySession := func(st *feedState) map[string]any {
		for _, l := range st.lines {
			if l["references"].(map[string]any)["session_id"] == sid && num(l["amount_minor"]) == 250000 {
				return l
			}
		}
		return nil
	}
	check(findBySession(feedA) != nil, "the payment did not reach Balance by sync")
	check(delivered("sync.available", isA) > hooksBefore, "no sync.available webhook for the payment")
	s.advance("24h")
	after := s.ctl("GET", "/nip/settlement", nil)
	check(num(after["sessions"]) > num(before["sessions"]), "no settlement session ran: %v then %v", before["sessions"], after["sessions"])
	check(len(after["problems"].([]any)) == 0, "settlement problems: %v", after["problems"])
	check(fmt.Sprint(s.ctl("GET", "/nip/transfers/"+sid, nil)["response_code"]) == "00", "the switch lost the transfer")
	feedA, _ = s.syncMore(idA, feedA, 500)
	line := findBySession(feedA)
	check(line != nil && line["status"] == "posted", "the payment is not posted after settlement: %v", line)
	postedKeys(t, feedA)
	if s.app != nil {
		checkSettled(t, s.app)
	}

	step(6, "own-account transfer from Bank A to Bank B matched by session ID")
	matches := func() int {
		debits := map[string]int64{}
		for _, l := range feedA.lines {
			if l["channel"] == "nip" && num(l["amount_minor"]) == -10000000 {
				debits[fmt.Sprint(l["references"].(map[string]any)["session_id"])] = num(l["amount_minor"])
			}
		}
		n := 0
		for _, l := range feedB.lines {
			if amt, ok := debits[fmt.Sprint(l["references"].(map[string]any)["session_id"])]; ok && amt == -num(l["amount_minor"]) {
				n++
			}
		}
		return n
	}
	matchedBefore := matches()
	s.ctl("POST", "/scenarios/own_account_transfer_across_banks", map[string]any{"persona": "adaeze"})
	s.advance("1m")
	feedA, _ = s.syncMore(idA, feedA, 500)
	feedB, _ = s.syncMore(idB, feedB, 500)
	check(matches() == matchedBefore+1, "want the new Bank B credit matched to its Bank A debit by session ID: %d before, %d after", matchedBefore, matches())
	snap := s.ctl("POST", "/snapshots", map[string]string{"name": "done-story"})
	atSnapA, atSnapB := idSet(feedA), idSet(feedB)

	step(7, "unstable_ids on: every transaction exactly once")
	changedBefore := len(feedA.modified) + len(feedB.modified)
	for _, inst := range []string{"bank-a", "bank-b"} {
		s.ctl("POST", "/scenarios/unstable_ids", map[string]any{"institution": inst})
	}
	for i := 0; i < 3; i++ {
		s.advance("26h")
		feedA, _ = s.syncMore(idA, feedA, 500)
		feedB, _ = s.syncMore(idB, feedB, 500)
	}
	refChanged := 0
	for _, m := range append(append([]map[string]any{}, feedA.modified...), feedB.modified...)[changedBefore:] {
		if m["modification_reason"] == "bank_reference_changed" {
			refChanged++
		}
	}
	check(refChanged > 0, "the scenario changed no bank references, so nothing was tested")
	for name, pair := range map[string]struct {
		st  *feedState
		was []string
	}{"Bank A": {feedA, atSnapA}, "Bank B": {feedB, atSnapB}} {
		postedKeys(t, pair.st)
		for _, id := range pair.was {
			_, ok := pair.st.lines[id]
			check(ok || pair.st.removed[id], "%s: transaction %s vanished", name, id)
		}
	}

	step(8, "she revokes consent in her bank's app")
	app := s.bankSignIn("adaeze")
	app.get("/apps")
	check(strings.Contains(app.last, "Balance") && strings.Contains(app.last, "via Beerus Connect"), "connected apps page: %.300s", app.last)
	consent := regexp.MustCompile(`/apps/(obc_[A-Za-z0-9]+)/revoke`).FindStringSubmatch(app.last)
	check(consent != nil, "the connected apps page has no consent to revoke: %.300s", app.last)
	app.post("/apps/"+consent[1]+"/revoke", url.Values{})
	check(strings.Contains(app.last, "Access withdrawn"), "revoke page: %.300s", app.last)
	check(s.must(s.call("GET", "/v1/connections/"+connA, nil), 200).JSON["status"] == "revoked", "Balance does not show the revoke")
	check(len(s.eventsOf("connection.status_changed", func(d map[string]any) bool {
		return d["connection_id"] == connA && d["status"] == "revoked" && d["error_code"] == "connection_revoked"
	})) > 0, "no reconnect-needed webhook")
	r := s.call("GET", "/v1/accounts/"+idA+"/transactions/sync", nil)
	check(r.Status == 409 && r.Code() == "connection_revoked", "sync after revoke: %d %s", r.Status, r.Body)
	check(s.must(s.call("GET", "/v1/connections/"+connB, nil), 200).JSON["status"] == "active", "the revoke touched the other bank")

	step(9, "a year passes: consent expires and can be renewed")
	oldCursor := feedB.cursor
	s.advance("366d")
	check(s.must(s.call("GET", "/v1/connections/"+connB, nil), 200).JSON["status"] == "expired", "Bank B consent did not expire")
	check(len(s.eventsOf("connection.status_changed", func(d map[string]any) bool {
		return d["connection_id"] == connB && d["status"] == "expired" && d["error_code"] == "reauth_required"
	})) > 0, "no expiry webhook")
	r = s.call("GET", "/v1/accounts/"+idB+"/transactions/sync", nil)
	check(r.Status == 409 && r.Code() == "reauth_required", "sync after expiry: %d %s", r.Status, r.Body)
	re := s.must(s.call("POST", "/v1/connections/"+connB+"/reauth", map[string]string{"state": "again"}), 201)
	res := s.completeLink(fmt.Sprint(re.JSON["widget_url"]), "", "adaeze", nil)
	check(res.Code != "" && res.State == "again", "renewal: %+v", res)
	ex := s.must(s.call("POST", "/v1/connections/exchange", map[string]any{"code": res.Code, "link_session_id": res.SessionID}), 200)
	check(ex.JSON["id"] == connB, "renewal created a new connection")
	s.syncNow()
	check(s.must(s.call("GET", "/v1/connections/"+connB, nil), 200).JSON["status"] == "active", "Bank B is not active after renewal")
	s.must(s.call("GET", "/v1/accounts/"+idB+"/transactions/sync?limit=5", nil), 200)

	step(10, "every response so far matched the OpenAPI contract the stub is held to")
	s.vmu.Lock()
	n := len(s.violations)
	s.vmu.Unlock()
	check(n == 0, "%d contract violations", n)

	step(11, "restoring the snapshot resets the cursor and rebuilds the feed")
	restored := s.ctl("POST", "/snapshots/"+fmt.Sprint(snap["id"])+"/restore", nil)
	check(restored["world_id"] != feedB.worldID, "restore kept the world ID")
	r = s.call("GET", "/v1/accounts/"+idB+"/transactions/sync?cursor="+oldCursor, nil)
	check(r.Status == 409 && r.Code() == "cursor_reset", "old cursor after restore: %d %s", r.Status, r.Body)
	s.syncNow()
	for name, pair := range map[string]struct {
		id  string
		was []string
	}{"Bank A": {idA, atSnapA}, "Bank B": {idB, atSnapB}} {
		fresh, last := s.syncAll(pair.id, "", 500)
		check(last.Status == 200, "%s: sync after restore: %d %s", name, last.Status, last.Body)
		check(fresh.worldID != feedB.worldID, "%s: world_id did not change", name)
		postedKeys(t, fresh)
		check(fmt.Sprint(idSet(fresh)) == fmt.Sprint(pair.was), "%s: the rebuilt feed differs from the feed at the snapshot: %d vs %d lines", name, len(fresh.lines), len(pair.was))
	}
	if s.app != nil {
		checkSettled(t, s.app)
		if c, err := s.app.VerifyBank(); err != nil || !c.Ledger.OK() || len(c.Mismatches) > 0 {
			t.Fatalf("bank ledgers at the end: %v %v %v", err, c.Ledger, c.Mismatches)
		}
	}
}
