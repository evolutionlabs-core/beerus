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
	limited := start.Before(today.AddDays(-s.cfg.Bank("bank-a").HistoryDays))

	step(1, "Adaeze's Bakery banks with Bank A and Bank B")
	pa, pb := s.personaAt("adaeze", "bank-a"), s.personaAt("adaeze", "bank-b")
	check(pa.Name == "Adaeze's Bakery Ltd" && len(pa.Accounts) == 2 && len(pb.Accounts) == 1, "customer: %+v %+v", pa, pb)
	check(pa.BVN != "" && pa.BVN == pb.BVN, "one customer holds BVNs %s and %s", pa.BVN, pb.BVN)
	current := pa.Accounts[0]
	ngozi := s.persona("ngozi").Accounts[0]
	s.pay("bank-a", current, "bank-b", ngozi, "40000")
	s.ctl("POST", "/faults", map[string]any{"kind": "timeout", "bank": "bank-b", "outcome": "failed", "count": 1})
	pending, _ := s.payTry("bank-a", current, "bank-b", ngozi, "15000", nil)
	check(pending["status"] != "success", "the timeout fault did not leave the transfer pending: %v", pending)
	resolved := s.ctl("POST", "/nip/sessions/"+fmt.Sprint(pending["session_id"])+"/status", nil)
	check(resolved["outcome"] == "reversed" && resolved["resolved_at"] != nil, "the status check did not reverse the transfer: %v", resolved)

	step(2, "Connect bank, pick Bank A, redirect to the bank")
	ls := s.must(s.call("POST", "/v1/link_sessions", map[string]any{"start_date": start.String(), "workspace_id": "ws_adaeze_bakery", "redirect_uri": s.redirect, "state": "st-done"}), 201)
	b := s.browser()
	b.get(fmt.Sprint(ls.JSON["widget_url"]))
	check(strings.Contains(b.last, "Bank A") && strings.Contains(b.last, "Bank B"), "the widget does not offer both banks: %.300s", b.last)
	b.post("/link/institution", url.Values{"institution": {"bank-a"}})
	check(b.result == nil && b.atBank(), "the widget did not send her to the bank: %v %.200s", b.cur, b.last)

	step(3, "sign in, 2FA code, CBN consent screen, pick accounts, approve")
	base := b.cur.Scheme + "://" + b.cur.Host
	b.postTo(base, "/login", url.Values{"username": {pa.Username}, "password": {pa.Password}})
	otp := reOTP.FindStringSubmatch(b.last)
	check(otp != nil, "no 2FA code in the inbox: %.300s", b.last)
	b.postTo(base, "/verify", url.Values{"otp": {otp[1]}})
	for _, want := range []string{s.cfg.Connect.LegalName, s.cfg.Connect.RegistryID, s.cfg.Connect.CACNumber, s.cfg.Partners[0].LegalName, s.cfg.Partners[0].Purpose, "Read only", "Anonymised analytics", "12 months", "Connected apps"} {
		check(strings.Contains(b.last, want), "the consent screen is missing %q", want)
	}
	check(!limited || strings.Contains(b.last, "shares history from"), "the consent screen does not say the bank limits history")
	offered := []string{}
	for _, m := range reAccount.FindAllStringSubmatch(b.last, -1) {
		offered = append(offered, m[1])
	}
	check(len(offered) == len(pa.Accounts), "the bank offered %d accounts, want %d", len(offered), len(pa.Accounts))
	b.postTo(base, "/authorize/consent", url.Values{"account": {current}, "action": {"approve"}})
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
	check(!limited || eff.After(start), "history was not limited: effective start %s, asked %s", eff, start)
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
	now := num(s.must(s.call("GET", "/v1/accounts/"+idA, nil), 200).JSON["balances"].(map[string]any)["current"].(map[string]any)["amount_minor"])
	check(opening == 0 && opening+sum == now, "opening %d + history %d != current %d", opening, sum, now)
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
	chidiB := s.persona("chidi").Accounts[0]
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
			check(verifyV1a(pub, h, s.realNow()) == nil, "webhook signature")
			n++
		}
		return n
	}
	isA := func(d map[string]any) bool { return d["account_id"] == idA }
	hooksBefore := delivered("sync.available", isA)
	pay, _, err := s.ctlWith("POST", "/banks/bank-b/transfers", map[string]string{"from": chidiB, "to_bank": "bank-a", "to": current, "amount": "2500", "narration": "Cake order"}, nil)
	check(err == nil, "payment: %v", err)
	sid := fmt.Sprint(pay["session_id"])
	check(len(sid) == 30 && pay["response_code"] == "00" && strings.HasPrefix(sid, "000002"), "payment reply: %v", pay)
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
	s.ctl("POST", "/nip/settle-now", nil)
	after := s.ctl("GET", "/nip/settlement", nil)
	check(num(after["sessions"]) > num(before["sessions"]), "no settlement session ran: %v then %v", before["sessions"], after["sessions"])
	check(len(after["problems"].([]any)) == 0, "settlement problems: %v", after["problems"])
	check(fmt.Sprint(s.ctl("GET", "/nip/sessions/"+sid, nil)["status"]) == "00", "the switch lost the transfer")
	feedA, _ = s.syncMore(idA, feedA, 500)
	line := findBySession(feedA)
	check(line != nil && line["status"] == "posted", "the payment is not posted after settlement: %v", line)
	postedKeys(t, feedA)
	if s.app != nil {
		checkSettled(t, s.app)
	}

	step(6, "own-account transfer from Bank A to Bank B matched by session ID")
	own := s.pay("bank-a", current, "bank-b", pb.Accounts[0], "100000")
	ownSID := []string{"", fmt.Sprint(own["session_id"])}
	check(len(ownSID[1]) == 30, "the transfer did not report a session ID: %v", own)
	s.syncNow()
	feedA, _ = s.syncMore(idA, feedA, 500)
	feedB, _ = s.syncMore(idB, feedB, 500)
	bySession := func(st *feedState) []map[string]any {
		out := []map[string]any{}
		for _, l := range st.lines {
			if l["channel"] == "nip" && l["references"].(map[string]any)["session_id"] == ownSID[1] {
				out = append(out, l)
			}
		}
		return out
	}
	debit, credit := bySession(feedA), bySession(feedB)
	check(len(debit) == 1 && len(credit) == 1 && num(debit[0]["amount_minor"]) == -num(credit[0]["amount_minor"]) && num(credit[0]["amount_minor"]) == 10000000, "session %s: Bank A %v, Bank B %v", ownSID[1], debit, credit)

	step(7, "she revokes consent in her bank's app")
	app := s.bankSignIn("adaeze")
	app.get("/apps")
	check(strings.Contains(app.last, "Balance") && strings.Contains(app.last, "via Beerus Connect"), "connected apps page: %.300s", app.last)
	consent := regexp.MustCompile(`/apps/(obc_[^/"]+)/revoke`).FindStringSubmatch(app.last)
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

	step(8, "a year passes: consent expires and can be renewed")
	if s.external {
		t.Log("A running beerus cannot move its clock, so the story stops before the consent expires.")
		return
	}
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

	step(9, "every response so far matched the OpenAPI contract the stub is held to")
	s.vmu.Lock()
	n := len(s.violations)
	s.vmu.Unlock()
	check(n == 0, "%d contract violations", n)

	step(10, "the settlement and the bank ledgers still balance")
	if s.app != nil {
		checkSettled(t, s.app)
		if c, err := s.app.VerifyBank(); err != nil || !c.OK() {
			t.Fatalf("bank ledgers at the end: %v %v", err, c)
		}
	}
}
