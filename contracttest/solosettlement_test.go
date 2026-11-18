package contracttest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

func TestSoloNIBSSServesSettlementReportsToTheirBank(t *testing.T) {
	s, banks := startSoloNIBSS(t, soloTSQAfter)
	s.registerBank("bank-a", "000014", "014", "Bank A")
	s.registerBank("bank-b", "000015", "015", "Bank B")
	ctx := t.Context()
	store, err := nibss.Open(ctx, s.dbURL, config.DatabaseName(s.dir, config.NIBSSID))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := func(h int) time.Time { return time.Date(2026, 11, 4, h, 0, 0, 0, clock.Lagos) }
	pay := func(id, from, to string, amount int64, key string, cutoff time.Time) {
		x := nibss.Session{ID: id, FromCode: from, ToCode: to, FromNUBAN: world.NUBAN("014", 1), ToNUBAN: world.NUBAN("015", 1), Amount: amount, Narration: "rent", CreatedAt: cutoff.Add(-time.Hour)}
		if _, _, err := store.Begin(ctx, x); err != nil {
			t.Fatal(err)
		}
		if err := store.Resolve(ctx, id, nibss.SessionOK, nip.OutcomeCredited, cutoff.Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkSettled(ctx, []string{id}, key, cutoff); err != nil {
			t.Fatal(err)
		}
	}
	pay("r1", "000014", "000015", 500, "2026-11-04T12:00", at(12))
	pay("r2", "000015", "000014", 200, "2026-11-04T12:00", at(12))
	pay("r3", "000014", "000015", 70, "2026-11-04T18:00", at(18))
	pay("r4", "000014", "000015", 9, "2026-11-04T23:00", at(23))
	for h, key := range map[int]string{12: "2026-11-04T12:00", 18: "2026-11-04T18:00"} {
		if err := store.MarkPosted(ctx, key, at(h)); err != nil {
			t.Fatal(err)
		}
	}
	errBody := func(code, message string) string {
		return fmt.Sprintf(`{"error":{"code":%q,"message":%q,"request_id":"req_ID"}}`, code, message)
	}
	row := func(key string, h int, outward, inward, net int64, transfers int) string {
		return fmt.Sprintf(`{"session_id":%q,"cutoff_at":"2026-11-04T%02d:00:00+01:00","posted_at":"2026-11-04T%02d:00:00+01:00","outward_minor":%d,"inward_minor":%d,"net_minor":%d,"transfers":%d}`, key, h, h, outward, inward, net, transfers)
	}
	noon, evening := row("2026-11-04T12:00", 12, 500, 200, -300, 2), row("2026-11-04T18:00", 18, 70, 0, -70, 1)
	url := s.dirURL + "/v1/settlement-reports"
	rows := []struct {
		name   string
		client *http.Client
		url    string
		status int
		want   string
	}{
		{"bank a reads its posted reports", banks["bank-a"], url, 200, `{"data":[` + noon + `,` + evening + `]}`},
		{"bank b reads its own", banks["bank-b"], url, 200, `{"data":[` + row("2026-11-04T12:00", 12, 200, 500, 300, 2) + `,` + row("2026-11-04T18:00", 18, 0, 70, 70, 1) + `]}`},
		{"after the first", banks["bank-a"], url + "?after=2026-11-04T12:00", 200, `{"data":[` + evening + `]}`},
		{"after the last", banks["bank-a"], url + "?after=2026-11-04T18:00", 200, `{"data":[]}`},
		{"after a session that is not posted", banks["bank-a"], url + "?after=2026-11-04T23:00", 200, `{"data":[` + noon + `,` + evening + `]}`},
		{"a bad cursor", banks["bank-a"], url + "?cursor=nope", 400, errBody("invalid_cursor", "That cursor is not valid. Use the next_cursor value from the last page.")},
		{"a bad limit", banks["bank-a"], url + "?limit=0", 400, errBody("invalid_request", "limit must be a number from 1 to 500.")},
		{"a stranger", banks["bank-z"], url, 403, errBody("forbidden", "This certificate may not read settlement reports. bank-z is not a bank NIBSS knows.")},
	}
	for _, r := range rows {
		status, got := s.call("nip", r.client, http.MethodGet, r.url, "", "")
		if status != r.status || got != r.want {
			t.Errorf("%s: got %d %s, want %d %s", r.name, status, got, r.status, r.want)
		}
	}
	positionURL := s.dirURL + "/v1/settlement-position"
	position := func(outward, inward, pending int64, transfers, settlements int) string {
		return fmt.Sprintf(`{"outward_minor":%d,"inward_minor":%d,"pending_inward_minor":%d,"transfers":%d,"settlements":%d}`, outward, inward, pending, transfers, settlements)
	}
	positions := []struct {
		name   string
		client *http.Client
		url    string
		status int
		want   string
	}{
		{"bank a position", banks["bank-a"], positionURL, 200, position(579, 200, 0, 4, 3)},
		{"bank b position", banks["bank-b"], positionURL, 200, position(200, 579, 0, 4, 3)},
		{"bank a after the first", banks["bank-a"], positionURL + "?after=2026-11-04T12:00", 200, position(79, 0, 0, 2, 2)},
		{"bank a after the last posted", banks["bank-a"], positionURL + "?after=2026-11-04T18:00", 200, position(9, 0, 0, 1, 1)},
		{"bank a after a session that is not posted", banks["bank-a"], positionURL + "?after=2026-11-04T23:00", 200, position(579, 200, 0, 4, 3)},
		{"position of a stranger", banks["bank-z"], positionURL, 403, errBody("forbidden", "This certificate may not read its settlement position. bank-z is not a bank NIBSS knows.")},
	}
	for _, r := range positions {
		status, got := s.call("nip", r.client, http.MethodGet, r.url, "", "")
		if status != r.status || got != r.want {
			t.Errorf("%s: got %d %s, want %d %s", r.name, status, got, r.status, r.want)
		}
	}
	open := s.control + "/v1/control/banks/%s/open-sessions"
	controls := []struct {
		name   string
		url    string
		token  string
		status int
		want   string
	}{
		{"open sessions of bank a", fmt.Sprintf(open, "bank-a"), s.token, 200, `{"bank_id":"bank-a","transfers":0,"outward_minor":0,"inward_minor":0,"pending_inward_minor":0,"unposted_settlements":1}`},
		{"open sessions of an unknown bank", fmt.Sprintf(open, "bank-z"), s.token, 404, errBody("not_found", "Bank bank-z is not in the NIBSS directory. Register it first.")},
		{"open sessions without a token", fmt.Sprintf(open, "bank-a"), "", 401, errBody("unauthenticated", "A valid control token is required.")},
	}
	for _, r := range controls {
		status, got := s.call("nip", s.plain, http.MethodGet, r.url, r.token, "")
		if status != r.status || got != r.want {
			t.Errorf("%s: got %d %s, want %d %s", r.name, status, got, r.status, r.want)
		}
	}
	status, got := s.call("participant", s.plain, http.MethodGet, s.control+"/v1/status", s.token, "")
	var st struct {
		Problems       []string `json:"problems"`
		PostedSessions *int     `json:"posted_sessions"`
	}
	wantProblem := "settlement session 2026-11-04T23:00 was cut off at 2026-11-04T23:00:00+01:00 and is not posted to the CBN yet"
	if err := json.Unmarshal([]byte(got), &st); err != nil || status != 200 || len(st.Problems) != 1 || st.Problems[0] != wantProblem || st.PostedSessions == nil || *st.PostedSessions != 2 {
		t.Errorf("status: got %d %s %v, want the problem %q and 2 posted sessions", status, got, err, wantProblem)
	}
	status, got = s.call("nip", banks["bank-a"], http.MethodGet, url+"?limit=1", "", "")
	var page struct {
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(got), &page); err != nil || status != 200 || page.NextCursor == "" {
		t.Fatalf("first page: %d %s %v", status, got, err)
	}
	status, got = s.call("nip", banks["bank-a"], http.MethodGet, url+"?limit=1&cursor="+page.NextCursor, "", "")
	if want := `{"data":[` + evening + `]}`; status != 200 || got != want {
		t.Errorf("second page: got %d %s, want 200 %s", status, got, want)
	}
	closureID := "000014261104100000123456789012"
	if status, got := s.call("nip", s.plain, http.MethodPut, s.control+"/v1/control/banks/bank-a/status", s.token, `{"status":"terminating"}`); status != http.StatusOK {
		t.Fatalf("terminate bank a: %d %s", status, got)
	}
	closure := nibss.Session{ID: closureID, FromCode: "000014", ToCode: "000015", FromNUBAN: world.NUBAN("014", 1), ToNUBAN: world.NUBAN("015", 1), Amount: 4000, Narration: "closure", CreatedAt: at(10), Closure: true}
	if _, _, err := store.Begin(ctx, closure); err != nil {
		t.Fatal(err)
	}
	banksURL := s.control + "/v1/control/banks"
	closures := banksURL + "/bank-a/closure-sessions"
	sessionURL := s.control + "/v1/control/sessions/" + closureID
	wantClosure := fmt.Sprintf(`{"session_id":%q,"from_account":%q,"to_bank_code":"000015","to_account":%q,"amount_minor":4000,"status":"pending","created_at":"CREATED"}`, closureID, closure.FromNUBAN, closure.ToNUBAN)
	wantSession := fmt.Sprintf(`{"session_id":%q,"from_code":"000014","to_code":"000015","from_nuban":%q,"to_nuban":%q,"amount_minor":4000,"narration":"closure","status":"pending","tsq_attempts":0,"created_at":"CREATED","closure":true}`, closureID, closure.FromNUBAN, closure.ToNUBAN)
	entry := func(id, code, nuban, name, state string) string {
		return fmt.Sprintf(`{"id":%q,"name":%q,"nip_code":%q,"nuban_code":%q,"type":"commercial","status":%q}`, id, name, code, nuban, state)
	}
	bankA, bankB := entry("bank-a", "000014", "014", "Bank A", "terminating"), entry("bank-b", "000015", "015", "Bank B", "active")
	createdAt := regexp.MustCompile(`"created_at":"([^"]*)"`)
	reads := []struct {
		name   string
		method string
		url    string
		token  string
		status int
		want   string
	}{
		{"the register", http.MethodGet, banksURL, s.token, 200, `{"data":[` + bankA + `,` + bankB + `]}`},
		{"the first bank of the register", http.MethodGet, banksURL + "?limit=1", s.token, 200, `{"data":[` + bankA + `],"next_cursor":"000014"}`},
		{"the next bank of the register", http.MethodGet, banksURL + "?limit=1&cursor=000014", s.token, 200, `{"data":[` + bankB + `]}`},
		{"the register after the last bank", http.MethodGet, banksURL + "?cursor=000015", s.token, 200, `{"data":[]}`},
		{"the register with a bad cursor", http.MethodGet, banksURL + "?cursor=nope", s.token, 400, errBody("invalid_cursor", "That cursor is not valid. Use the next_cursor value from the last page.")},
		{"the register with a bad limit", http.MethodGet, banksURL + "?limit=501", s.token, 400, errBody("invalid_request", "limit must be a number from 1 to 500.")},
		{"the register without a token", http.MethodGet, banksURL, "", 401, errBody("unauthenticated", "A valid control token is required.")},
		{"the register with a post", http.MethodPost, banksURL, s.token, 405, errBody("method_not_allowed", "Use GET for this path.")},
		{"one bank", http.MethodGet, banksURL + "/bank-a", s.token, 200, bankA},
		{"an unknown bank", http.MethodGet, banksURL + "/bank-z", s.token, 404, errBody("not_found", "Bank bank-z is not in the NIBSS directory. Register it first.")},
		{"one bank without a token", http.MethodGet, banksURL + "/bank-a", "", 401, errBody("unauthenticated", "A valid control token is required.")},
		{"one bank with a delete", http.MethodDelete, banksURL + "/bank-a", s.token, 405, errBody("method_not_allowed", "Use GET or PUT for this path.")},
		{"closure sessions of bank a", http.MethodGet, closures, s.token, 200, `{"data":[` + wantClosure + `]}`},
		{"closure sessions of bank b", http.MethodGet, banksURL + "/bank-b/closure-sessions", s.token, 200, `{"data":[]}`},
		{"closure sessions after the last", http.MethodGet, closures + "?cursor=" + closureID, s.token, 200, `{"data":[]}`},
		{"closure sessions with a bad cursor", http.MethodGet, closures + "?cursor=nope", s.token, 400, errBody("invalid_cursor", "That cursor is not valid. Use the next_cursor value from the last page.")},
		{"closure sessions with a bad limit", http.MethodGet, closures + "?limit=0", s.token, 400, errBody("invalid_request", "limit must be a number from 1 to 500.")},
		{"closure sessions of an unknown bank", http.MethodGet, banksURL + "/bank-z/closure-sessions", s.token, 404, errBody("not_found", "Bank bank-z is not in the NIBSS directory. Register it first.")},
		{"closure sessions without a token", http.MethodGet, closures, "", 401, errBody("unauthenticated", "A valid control token is required.")},
		{"closure sessions with a post", http.MethodPost, closures, s.token, 405, errBody("method_not_allowed", "Use GET for this path.")},
		{"one session", http.MethodGet, sessionURL, s.token, 200, wantSession},
		{"an unknown session", http.MethodGet, s.control + "/v1/control/sessions/000014261104100000999999999999", s.token, 404, errBody("not_found", "Session 000014261104100000999999999999 is not in the NIBSS records.")},
		{"one session without a token", http.MethodGet, sessionURL, "", 401, errBody("unauthenticated", "A valid control token is required.")},
		{"one session with a put", http.MethodPut, sessionURL, s.token, 405, errBody("method_not_allowed", "Use GET for this path.")},
		{"open sessions of bank b with a pending closure", http.MethodGet, fmt.Sprintf(open, "bank-b"), s.token, 200, `{"bank_id":"bank-b","transfers":1,"outward_minor":0,"inward_minor":0,"pending_inward_minor":4000,"unposted_settlements":1}`},
	}
	for _, r := range reads {
		spec := "nip"
		if r.status == http.StatusMethodNotAllowed {
			spec = ""
		}
		status, got := s.call(spec, s.plain, r.method, r.url, r.token, "")
		if m := createdAt.FindStringSubmatch(got); m != nil {
			if when, err := time.Parse(time.RFC3339, m[1]); err != nil || !when.Equal(closure.CreatedAt) {
				t.Errorf("%s: created_at %s, want %s: %v", r.name, m[1], closure.CreatedAt.Format(time.RFC3339), err)
			}
			got = createdAt.ReplaceAllString(got, `"created_at":"CREATED"`)
		}
		if status != r.status || got != r.want {
			t.Errorf("%s: got %d %s, want %d %s", r.name, status, got, r.status, r.want)
		}
	}
}
