package contracttest

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

func (s *Stack) bankDB(bank string) *pgx.Conn {
	s.t.Helper()
	pc, err := pgx.ParseConfig(s.cfg.DatabaseURL)
	if err != nil {
		s.t.Fatal(err)
	}
	pc.Database = s.cfg.Bank(bank).Database
	db, err := pgx.ConnectConfig(context.Background(), pc)
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { db.Close(context.Background()) })
	return db
}

func (s *Stack) consentTrail(bank string) map[string]bool {
	seen := map[string]bool{}
	for _, e := range s.ctl("GET", "/registry/consents?bank="+bank, nil)["data"].([]any) {
		m := e.(map[string]any)
		seen[fmt.Sprint(m["consent_id"])+" "+fmt.Sprint(m["status"])+"/"+fmt.Sprint(m["via"])] = true
	}
	return seen
}

func (s *Stack) waitConsent(bank, key string) {
	s.t.Helper()
	waitFor(s.t, waitLimit, "the NIBSS consent ledger of "+bank+" to hold "+key, func() bool { return s.consentTrail(bank)[key] })
}

func (s *Stack) hasRevoked(connID string) bool {
	return len(s.eventsOf("connection.status_changed", func(d map[string]any) bool {
		return d["connection_id"] == connID && d["status"] == "revoked"
	})) > 0
}

func TestRealAggregator(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newBankStack(t)
	db := s.bankDB("bank-a")
	s.spend(s.persona("adaeze"), 3)
	conn := s.connect("adaeze", "bank-a", s.today().AddDays(-30))
	id := fmt.Sprint(accountAt(t, conn, "current")["id"])

	var consentID string
	if err := db.QueryRow(context.Background(), `SELECT id FROM ob_consents WHERE status = 'Authorised' AND partner_id = 'balance'`).Scan(&consentID); err != nil {
		t.Fatalf("the bank holds no consent for the connection: %v", err)
	}
	s.waitConsent("bank-a", consentID+" Authorised/bank")

	st, _ := s.syncAll(id, "", 500)
	if len(st.lines) != 13 {
		t.Fatalf("%d lines synced over the bank API, want a deposit and 3 transfers of 4 lines", len(st.lines))
	}
	var used int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM ob_consents WHERE id = $1 AND last_used_at IS NOT NULL`, consentID).Scan(&used); err != nil || used != 1 {
		t.Fatalf("the bank never saw a resource call: %d %v", used, err)
	}

	if err := s.agg.Save(); err != nil {
		t.Fatal(err)
	}
	state, err := os.ReadFile(s.cfg.Path("connect", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(state), "oba_") || strings.Contains(string(state), "obr_") || !strings.Contains(string(state), `"vault"`) {
		t.Fatal("bank tokens must be in the vault, encrypted")
	}
	if info, err := os.Stat(s.cfg.Path("connect", "vault.key")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("vault master key: %v %v", info, err)
	}

	refreshRows := func() (total, rotated int) {
		t.Helper()
		if err := db.QueryRow(context.Background(), `SELECT count(*), count(*) FILTER (WHERE rotated_at IS NOT NULL) FROM ob_tokens WHERE kind = 'refresh'`).Scan(&total, &rotated); err != nil {
			t.Fatal(err)
		}
		return total, rotated
	}
	before, rotatedBefore := refreshRows()
	waitFor(t, waitLimit, "Connect to refresh the access token", func() bool {
		s.syncNow()
		total, _ := refreshRows()
		return total > before
	})
	if _, rotated := refreshRows(); rotated <= rotatedBefore {
		t.Fatalf("the bank did not rotate the previous refresh token: %d rotated before, %d after", rotatedBefore, rotated)
	}
	s.spend(s.persona("adaeze"), 1)
	waitFor(t, waitLimit, "the refreshed access token to fetch the new transfer", func() bool {
		s.syncNow()
		st, _ := s.syncAll(id, "", 500)
		return len(st.lines) == 17
	})
	if got := s.must(s.call("GET", "/v1/connections/"+conn.ConnectionID, nil), 200); got.JSON["status"] != "active" {
		t.Fatalf("status after refresh: %v", got.JSON["status"])
	}

	forged, err := openbanking.Sign(mustSigningKey(t, s), "bank-a", map[string]any{"iss": s.cfg.Bank("bank-a").APIURL(), "aud": s.cfg.WidgetURL(), "jti": "x", "exp": time.Now().Add(time.Minute).Unix(), "consent_id": consentID, "status": "Revoked"})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"nonsense", forged} {
		resp, err := s.raw.Post(s.cfg.WidgetURL()+"/consent-events", "application/secevent+jwt", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode < 400 {
			t.Fatalf("an event not signed by the bank was accepted: %d", resp.StatusCode)
		}
	}
	if got := s.must(s.call("GET", "/v1/connections/"+conn.ConnectionID, nil), 200); got.JSON["status"] != "active" {
		t.Fatalf("forged event changed the connection to %v", got.JSON["status"])
	}

	b := s.bankSignIn("adaeze")
	b.get("/apps")
	m := regexp.MustCompile(`/apps/(obc_[A-Za-z0-9_-]+)/revoke`).FindStringSubmatch(b.last)
	if m == nil || m[1] != consentID {
		t.Fatalf("connected apps page does not offer to revoke %s: %.500s", consentID, b.last)
	}
	b.post("/apps/"+consentID+"/revoke", map[string][]string{})
	if !strings.Contains(b.last, "Access withdrawn") {
		t.Fatalf("revoke page: %.300s", b.last)
	}
	if got := s.must(s.call("GET", "/v1/connections/"+conn.ConnectionID, nil), 200); got.JSON["status"] != "revoked" {
		t.Fatalf("status after the customer revoked at the bank: %v", got.JSON["status"])
	}
	if !s.hasRevoked(conn.ConnectionID) {
		t.Fatal("no reconnect-needed webhook after the bank's consent event")
	}
	s.waitConsent("bank-a", consentID+" Revoked/bank")
	if r := s.call("GET", "/v1/accounts/"+id, nil); r.Status != 409 || r.Code() != "connection_revoked" {
		t.Fatalf("account after revoke: %d %s", r.Status, r.Body)
	}

	conn2 := s.connect("adaeze", "bank-a", s.today().AddDays(-30))
	if fmt.Sprint(accountAt(t, conn2, "current")["id"]) != id {
		t.Fatal("account ID changed across the reconnect")
	}
	if err := db.QueryRow(context.Background(), `SELECT id FROM ob_consents WHERE status = 'Authorised' AND partner_id = 'balance'`).Scan(&consentID); err != nil {
		t.Fatal(err)
	}
	s.must(s.call("DELETE", "/v1/connections/"+conn2.ConnectionID, nil), 204)
	var status, via string
	if err := db.QueryRow(context.Background(), `SELECT status, revoked_via FROM ob_consents WHERE id = $1`, consentID).Scan(&status, &via); err != nil || status != "Revoked" || via != "aggregator" {
		t.Fatalf("consent at the bank after the partner disconnected: %s %s %v", status, via, err)
	}
	s.waitConsent("bank-a", consentID+" Revoked/aggregator")
	if r := s.call("GET", "/v1/connections/"+conn2.ConnectionID, nil); r.JSON["status"] != "revoked" {
		t.Fatalf("status after disconnect: %s", r.Body)
	}

	conn4 := s.connect("adaeze", "bank-a", s.today().AddDays(-30))
	if err := db.QueryRow(context.Background(), `SELECT id FROM ob_consents WHERE status = 'Authorised' AND partner_id = 'balance'`).Scan(&consentID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(context.Background(), `UPDATE ob_consents SET expires_at = '2000-01-01' WHERE id = $1`, consentID); err != nil {
		t.Fatal(err)
	}
	s.syncNow()
	if r := s.call("GET", "/v1/connections/"+conn4.ConnectionID, nil); r.JSON["status"] != "expired" {
		t.Fatalf("a consent that expired at the bank must expire the connection: %s", r.Body)
	}
	if len(s.eventsOf("connection.status_changed", func(d map[string]any) bool {
		return d["connection_id"] == conn4.ConnectionID && d["status"] == "expired" && d["error_code"] == "reauth_required"
	})) == 0 {
		t.Fatal("no reauth_required webhook after the consent expired")
	}
	s.waitConsent("bank-a", consentID+" ReconsentRequired/bank")

	conn3 := s.connect("adaeze", "bank-a", s.today().AddDays(-30))
	if _, err := db.Exec(context.Background(), `UPDATE ob_consents SET status = 'Revoked', revoked_via = 'bank' WHERE status = 'Authorised' AND partner_id = 'balance'`); err != nil {
		t.Fatal(err)
	}
	s.syncNow()
	if r := s.call("GET", "/v1/connections/"+conn3.ConnectionID, nil); r.JSON["status"] != "revoked" {
		t.Fatalf("a consent_revoked answer on a resource call must revoke the connection: %s", r.Body)
	}
	if !s.hasRevoked(conn3.ConnectionID) {
		t.Fatal("no reconnect-needed webhook after consent_revoked on a resource call")
	}
}

func mustSigningKey(t *testing.T, s *Stack) *ecdsa.PrivateKey {
	t.Helper()
	k, err := s.cfg.SigningKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestConsentEventsNeedAKnownBank(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newBankStack(t)
	conn := s.connect("adaeze", "bank-a", s.today().AddDays(-30))
	var consentID string
	if err := s.bankDB("bank-a").QueryRow(context.Background(), `SELECT id FROM ob_consents WHERE status = 'Authorised' AND partner_id = 'balance'`).Scan(&consentID); err != nil {
		t.Fatal(err)
	}
	aURL, bURL, widget := s.cfg.Bank("bank-a").APIURL(), s.cfg.Bank("bank-b").APIURL(), s.cfg.WidgetURL()
	event := func(iss, aud, jti string) string {
		return fmt.Sprintf(`{"iss":%q,"aud":%q,"jti":%q,"exp":%d,"consent_id":%q,"status":"Revoked"}`, iss, aud, jti, time.Now().Add(time.Minute).Unix(), consentID)
	}
	invalid := `"code":"invalid_request","message":"The event is not valid."`
	rows := []struct {
		name   string
		caller string
		body   string
		status int
		want   string
	}{
		{"no caller", "", event(aURL, widget, "j1"), http.StatusUnauthorized, `"code":"unauthenticated","message":"Send your ID in the Beerus-Caller header."`},
		{"an unknown bank", "bank-z", event(aURL, widget, "j2"), http.StatusForbidden, `"code":"forbidden","message":"bank-z is not a bank Connect knows."`},
		{"bank-a naming bank-b as issuer", "bank-a", event(bURL, widget, "j3"), http.StatusBadRequest, invalid},
		{"bank-a naming another audience", "bank-a", event(aURL, "http://other.localhost", "j4"), http.StatusBadRequest, invalid},
		{"bank-b reporting a consent of bank-a", "bank-b", event(bURL, widget, "j5"), http.StatusNoContent, ""},
	}
	for _, r := range rows {
		req, err := http.NewRequest(http.MethodPost, widget+"/consent-events", strings.NewReader(r.body))
		if err != nil {
			t.Fatal(err)
		}
		if r.caller != "" {
			req.Header.Set(config.CallerHeader, r.caller)
		}
		resp, err := s.raw.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != r.status || !strings.Contains(string(body), r.want) {
			t.Fatalf("%s: %d %s %v, want %d containing %s", r.name, resp.StatusCode, body, err, r.status, r.want)
		}
	}
	if got := s.must(s.call("GET", "/v1/connections/"+conn.ConnectionID, nil), 200); got.JSON["status"] != "active" {
		t.Fatalf("status %v", got.JSON["status"])
	}
}

func TestConsentEventsAreKeptAndRetried(t *testing.T) {
	if testingExternal() {
		t.Skip("needs an in-process stack")
	}
	s := newBankStack(t)
	db := s.bankDB("bank-a")
	conn := s.connect("adaeze", "bank-a", s.today().AddDays(-30))
	var consentID string
	if err := db.QueryRow(context.Background(), `SELECT id FROM ob_consents WHERE status = 'Authorised' AND partner_id = 'balance'`).Scan(&consentID); err != nil {
		t.Fatal(err)
	}
	s.whileConnectIsDown(func() {
		b := s.bankSignIn("adaeze")
		b.post("/apps/"+consentID+"/revoke", map[string][]string{})
		var waiting, attempts int
		waitFor(t, waitLimit, "the revoke event to fail once", func() bool {
			err := db.QueryRow(context.Background(), `SELECT count(*), coalesce(max(attempts), 0) FROM ob_events WHERE delivered_at IS NULL AND consent_id = $1 AND status = 'Revoked'`, consentID).Scan(&waiting, &attempts)
			return err == nil && waiting == 1 && attempts >= 1
		})
	})
	var left, delivered int
	var err error
	waitFor(t, waitLimit, "the revoke event to be delivered again", func() bool {
		err = db.QueryRow(context.Background(), `SELECT count(*) FILTER (WHERE delivered_at IS NULL), count(*) FILTER (WHERE delivered_at IS NOT NULL) FROM ob_events WHERE consent_id = $1`, consentID).Scan(&left, &delivered)
		return err == nil && left == 0 && delivered >= 2
	})
	if err != nil || left != 0 || delivered < 2 {
		t.Fatalf("events after the retry: %d undelivered, %d delivered, %v", left, delivered, err)
	}
	if got := s.must(s.call("GET", "/v1/connections/"+conn.ConnectionID, nil), 200); got.JSON["status"] != "revoked" {
		t.Fatalf("status after the retried event: %v", got.JSON["status"])
	}
}

func TestBankLedgerOutboxInPostgres(t *testing.T) {
	s := newBankStack(t)
	ctx := context.Background()
	pc, err := pgxpool.ParseConfig(s.cfg.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	pc.ConnConfig.Database = s.cfg.Bank("bank-a").Database
	db, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := &openbanking.PGStore{Pool: func() *pgxpool.Pool { return db }}
	now := time.Now()
	for _, status := range []openbanking.Status{openbanking.Authorised, openbanking.Revoked} {
		if err := store.EnqueueLedger(ctx, openbanking.ConsentEvent{ConsentID: "obc_outbox", Bank: "bank-a", ClientID: "OBR-1", Status: status, Via: "bank", At: now}, now); err != nil {
			t.Fatal(err)
		}
	}
	due, err := store.LedgerDue(ctx, now, 10)
	if err != nil || len(due) != 2 || due[0].Status != openbanking.Authorised || due[1].Status != openbanking.Revoked {
		t.Fatalf("ledger events %+v: %v", due, err)
	}
	if connect, _ := store.Due(ctx, now, 100); slices.ContainsFunc(connect, func(e openbanking.Event) bool { return e.ConsentID == "obc_outbox" }) {
		t.Fatal("a ledger event is due for Connect")
	}
	if err := store.Retry(ctx, due[0].ID, 1, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if held, _ := store.LedgerDue(ctx, now, 10); len(held) != 0 {
		t.Fatalf("later events are due while the first waits: %+v", held)
	}
	if err := store.Retry(ctx, due[0].ID, 1, now); err != nil {
		t.Fatal(err)
	}
	for _, e := range due {
		if err := store.Delivered(ctx, e.ID); err != nil {
			t.Fatal(err)
		}
	}
	if left, _ := store.LedgerDue(ctx, now, 10); len(left) != 0 {
		t.Fatalf("delivered events still due: %+v", left)
	}
}
