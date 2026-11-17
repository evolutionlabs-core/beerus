package contracttest

import (
	"encoding/json"
	"fmt"
	"net/http"
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
	status, got := s.call("nip", banks["bank-a"], http.MethodGet, url+"?limit=1", "", "")
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
}
