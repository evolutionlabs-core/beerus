package contracttest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
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

func TestJourney(t *testing.T) {
	s := sharedStack(t)
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
	start := s.linkStart()
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
	check(resolved["status"] == "25" && resolved["outcome"] == "failed" && resolved["resolved_at"] != nil, "the status check did not fail the transfer: %v", resolved)

	step(2, "Connect bank, pick Bank A, redirect to the bank")
	b := s.browser()
	b.get(s.linkURL("ws_adaeze_bakery", "st-done"))
	check(strings.Contains(b.last, "Bank A") && strings.Contains(b.last, "Bank B"), "the widget does not offer both banks: %.300s", b.last)
	b.post("/link/institution", url.Values{"institution": {"bank-a"}})
	check(b.result == nil && b.atBank(), "the widget did not send her to the bank: %v %.200s", b.cur, b.last)

	step(3, "sign in, CBN consent screen, pick accounts, approve")
	base := b.cur.Scheme + "://" + b.cur.Host
	b.postTo(base, "/login", url.Values{"username": {pa.Username}, "password": {pa.Password}})
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
	check(b.result == nil && strings.Contains(b.last, "Pick accounts"), "the widget did not ask which accounts to share: %.300s", b.last)
	b.pickAccounts()
	check(b.result != nil && b.result.Query().Get("public_token") != "", "no public_token on the redirect to Balance: %.300s", b.last)
	q := b.result.Query()
	check(q.Get("state") == "st-done" && q.Get("link_session_id") != "", "redirect: %v", b.result)
	conn := s.must(s.call("POST", "/v1/public_token/exchange", map[string]any{"public_token": q.Get("public_token")}), 200)
	connA := fmt.Sprint(conn.JSON["connection_id"])
	tokA := fmt.Sprint(conn.JSON["access_token"])
	s.syncNow()
	check(s.must(s.call("GET", "/v1/connection", nil, withToken(tokA)), 200).JSON["status"] == "active", "Bank A connection not active")
	accts := s.must(s.call("GET", "/v1/accounts", nil, withToken(tokA)), 200).JSON["data"].([]any)
	check(len(accts) == 1 && accts[0].(map[string]any)["type"] == "current", "only the picked account should be shared: %v", accts)
	idA := fmt.Sprint(accts[0].(map[string]any)["id"])

	step(4, "opening balance and history up to the bank's limit")
	feedA, _ := s.syncAll(tokA, idA, "", 500)
	small, _ := s.syncAll(tokA, idA, "", 7)
	check(small.pages >= 2 && len(small.lines) == len(feedA.lines), "paging changed the result: %d pages, %d vs %d lines", small.pages, len(small.lines), len(feedA.lines))
	for lid, l := range feedA.lines {
		check(fmt.Sprint(small.lines[lid]) == fmt.Sprint(l), "line %s differs between page sizes", lid)
	}
	again, quiet := s.syncMore(tokA, idA, feedA, 100)
	check(quiet.Status == 200 && len(again.lines) == len(feedA.lines) && len(quiet.JSON["added"].([]any))+len(quiet.JSON["modified"].([]any))+len(quiet.JSON["removed"].([]any)) == 0, "a second sync with no changes returned data: %s", quiet.Body)
	eff := s.firstDate(idA, start)
	check(!limited || eff.After(start), "history was not limited: effective start %s, asked %s", eff, start)
	opening := num(s.must(s.call("GET", fmt.Sprintf("/v1/accounts/%s/balance?as_of=%s", idA, eff.AddDays(-1)), nil, withToken(tokA)), 200).JSON["ledger_balance"].(map[string]any)["amount_minor"])
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
	now := num(s.must(s.call("GET", "/v1/accounts/"+idA, nil, withToken(tokA)), 200).JSON["balances"].(map[string]any)["current"].(map[string]any)["amount_minor"])
	check(opening == 0 && opening+sum == now, "opening %d + history %d != current %d", opening, sum, now)
	upto := opening
	for _, l := range posted {
		if bt, _ := time.Parse(time.RFC3339, fmt.Sprint(l["booked_at"])); clock.DateOf(bt).Before(today) {
			upto += num(l["amount_minor"])
		}
	}
	yesterday := s.must(s.call("GET", fmt.Sprintf("/v1/accounts/%s/balance?as_of=%s", idA, today.AddDays(-1)), nil, withToken(tokA)), 200)
	check(num(yesterday.JSON["ledger_balance"].(map[string]any)["amount_minor"]) == upto, "as_of yesterday does not match the posted lines")
	for _, want := range []string{"Transfer fee", "VAT on transfer fee", "Stamp duty"} {
		check(kinds[want], "no %q line in the history", want)
	}
	check(reversal, "no reversal in the history")
	if s.app != nil {
		checkSettled(t, s.app)
	}

	b2 := s.connect("adaeze", "bank-b")
	idB := fmt.Sprint(b2.Accounts[0]["id"])
	feedB, _ := s.syncAll(b2.AccessToken, idB, "", 500)
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
			check(verifyV1a(pub, h, time.Now()) == nil, "webhook signature")
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
	feedA, _ = s.syncMore(tokA, idA, feedA, 500)
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
	feedA, _ = s.syncMore(tokA, idA, feedA, 500)
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
	feedA, _ = s.syncMore(tokA, idA, feedA, 500)
	feedB, _ = s.syncMore(b2.AccessToken, idB, feedB, 500)
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
	check(s.must(s.call("GET", "/v1/connection", nil, withToken(tokA)), 200).JSON["status"] == "revoked", "Balance does not show the revoke")
	check(len(s.eventsOf("connection.status_changed", func(d map[string]any) bool {
		return d["connection_id"] == connA && d["status"] == "revoked" && d["error_code"] == "connection_revoked"
	})) > 0, "no reconnect-needed webhook")
	r := s.call("GET", "/v1/accounts/"+idA+"/transactions/sync", nil, withToken(tokA))
	check(r.Status == 409 && r.Code() == "connection_revoked", "sync after revoke: %d %s", r.Status, r.Body)
	check(s.must(s.call("GET", "/v1/connection", nil, withToken(b2.AccessToken)), 200).JSON["status"] == "active", "the revoke touched the other bank")

	if !s.external {
		step(8, "a first consent has no effective start date and as_of stays inside the window")
		fresh := s.connect("chidi", "bank-b")
		idF := fmt.Sprint(fresh.Accounts[0]["id"])
		cF := s.must(s.call("GET", "/v1/connection", nil, withToken(fresh.AccessToken)), 200)
		check(cF.JSON["effective_start_date"] == nil, "effective_start_date %v, want null", cF.JSON["effective_start_date"])
		for _, d := range []clock.Date{start.AddDays(-2), today} {
			r := s.call("GET", fmt.Sprintf("/v1/accounts/%s/balance?as_of=%s", idF, d), nil, withToken(fresh.AccessToken))
			check(r.Status == 422 && r.Code() == "as_of_out_of_range", "as_of %s: %d %s", d, r.Status, r.Body)
		}
		check(s.call("GET", "/v1/accounts/"+idF+"/balance?as_of=yesterday", nil, withToken(fresh.AccessToken)).Status == 400, "a bad as_of was accepted")
	}

	if s.sink != nil {
		step(9, "every webhook is signed, matches the spec and carries its headers")
		pub := s.publicKey()
		seen := map[string]bool{}
		for _, h := range s.sink.all() {
			check(verifyV1a(pub, h, time.Now()) == nil, "webhook signature")
			var ev struct {
				Type string `json:"type"`
			}
			check(json.Unmarshal(h.Body, &ev) == nil, "webhook body: %s", h.Body)
			seen[ev.Type] = true
			validateWebhook(t, ev.Type, h.Body)
			for _, name := range []string{"webhook-id", "webhook-timestamp", "webhook-signature"} {
				check(h.Header.Get(name) != "", "webhook %s has no %s header", ev.Type, name)
			}
		}
		for _, want := range []string{"account.added", "historical_sync.completed", "sync.available", "connection.status_changed"} {
			check(seen[want], "no %s webhook", want)
		}
	}

	step(10, "the settlement and the bank ledgers still balance")
	if s.app != nil {
		checkSettled(t, s.app)
		if c, err := s.app.VerifyBank(context.Background()); err != nil || !c.OK() {
			t.Fatalf("bank ledgers at the end: %v %v", err, c)
		}
	}
}
