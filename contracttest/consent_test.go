package contracttest

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

const latestConsent = `SELECT id FROM ob_consents WHERE status = 'Authorised' AND partner_id = 'balance' ORDER BY created_at DESC LIMIT 1`

type bankBrowser struct {
	t    *testing.T
	base string
	c    *http.Client
	last string
	loc  string
	next string
}

var (
	reBankCSRF = regexp.MustCompile(`name="csrf" value="([^"]+)"`)
	reConsent  = regexp.MustCompile(`/apps/(obc_[A-Za-z0-9_-]+)/revoke`)
)

func (s *Stack) bankBrowserAt(bank string) *bankBrowser {
	jar, _ := cookiejar.New(nil)
	return &bankBrowser{t: s.t, base: s.cfg.Bank(bank).URL(), c: &http.Client{Jar: jar, Timeout: 30 * time.Second, Transport: s.raw.Transport}}
}

func (b *bankBrowser) do(req *http.Request) {
	b.t.Helper()
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	b.last, b.loc, b.next = string(body), resp.Request.URL.Path, resp.Header.Get("Location")
}

func (b *bankBrowser) get(path string) {
	b.t.Helper()
	req, _ := http.NewRequest("GET", b.base+path, nil)
	b.do(req)
}

func (b *bankBrowser) post(path string, form url.Values) {
	b.t.Helper()
	if m := reBankCSRF.FindStringSubmatch(b.last); m != nil && form.Get("csrf") == "" {
		form.Set("csrf", m[1])
	}
	req, _ := http.NewRequest("POST", b.base+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", b.base)
	b.do(req)
}

func (s *Stack) signInAt(bank, id string) *bankBrowser {
	s.t.Helper()
	b := s.bankBrowserAt(bank)
	p := s.personaAt(id, bank)
	b.get("/login")
	b.post("/login", url.Values{"username": {p.Username}, "password": {p.Password}})
	m := reOTP.FindStringSubmatch(b.last)
	if m == nil {
		s.t.Fatalf("no code on the verify page: %.300s", b.last)
	}
	b.post("/verify", url.Values{"otp": {m[1]}})
	if b.loc != "/accounts" {
		s.t.Fatalf("sign-in ended at %s", b.loc)
	}
	return b
}

func (s *Stack) bankSignIn(id string) *bankBrowser {
	s.t.Helper()
	return s.signInAt("bank-a", id)
}

func (s *Stack) bankDB(bank string) *pgx.Conn {
	s.t.Helper()
	pc, err := pgx.ParseConfig(s.cfg.DatabaseURL)
	if err != nil {
		s.t.Fatal(err)
	}
	pc.Database = s.cfg.Bank(bank).Database
	db, err := pgx.ConnectConfig(s.t.Context(), pc)
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { db.Close(context.WithoutCancel(s.t.Context())) })
	return db
}

func (s *Stack) consentID(db *pgx.Conn) string {
	s.t.Helper()
	var id string
	if err := db.QueryRow(s.t.Context(), latestConsent).Scan(&id); err != nil {
		s.t.Fatalf("the bank holds no consent: %v", err)
	}
	return id
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

func (s *Stack) signingKey() *ecdsa.PrivateKey {
	s.t.Helper()
	k, err := s.cfg.SigningKey()
	if err != nil {
		s.t.Fatal(err)
	}
	return k
}

func TestConsentEvents(t *testing.T) {
	s := sharedStack(t)
	s.needsNodes()

	s.run("tokens, revoke and expiry", func(s *Stack) {
		ctx := t.Context()
		db := s.bankDB("bank-a")
		s.spend(s.persona("adaeze"), 3)
		conn := s.connect("adaeze", "bank-a")
		id := s.id(conn, "current")
		consent := s.consentID(db)
		s.waitConsent("bank-a", consent+" Authorised/bank")
		if st, _ := s.syncAll(conn.AccessToken, id, "", 500); len(st.lines) != 13 {
			t.Fatalf("%d lines synced over the bank API, want a deposit and 3 transfers of 4 lines", len(st.lines))
		}
		var used int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM ob_consents WHERE id = $1 AND last_used_at IS NOT NULL`, consent).Scan(&used); err != nil || used != 1 {
			t.Fatalf("the bank never saw a resource call: %d %v", used, err)
		}
		if err := s.agg.Save(); err != nil {
			t.Fatal(err)
		}
		state, err := os.ReadFile(s.cfg.Path("connect", "state.json"))
		if err != nil || strings.Contains(string(state), "oba_") || strings.Contains(string(state), "obr_") || !strings.Contains(string(state), `"vault"`) {
			t.Fatalf("bank tokens must be in the vault, encrypted: %v", err)
		}
		if info, err := os.Stat(s.cfg.Path("connect", "vault.key")); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("vault master key: %v %v", info, err)
		}
		refreshRows := func() (total, rotated int) {
			if err := db.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE rotated_at IS NOT NULL) FROM ob_tokens WHERE kind = 'refresh'`).Scan(&total, &rotated); err != nil {
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
			st, _ := s.syncAll(conn.AccessToken, id, "", 500)
			return len(st.lines) == 17
		})

		forged, err := openbanking.Sign(s.signingKey(), "bank-a", map[string]any{"iss": s.cfg.Bank("bank-a").APIURL(), "aud": s.cfg.WidgetURL(), "jti": "x", "exp": time.Now().Add(time.Minute).Unix(), "consent_id": consent, "status": "Revoked"})
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
		if got := s.status(conn.AccessToken); got != "active" {
			t.Fatalf("forged event changed the connection to %v", got)
		}

		b := s.bankSignIn("adaeze")
		b.get("/apps")
		if m := reConsent.FindStringSubmatch(b.last); m == nil || m[1] != consent {
			t.Fatalf("connected apps page does not offer to revoke %s: %.500s", consent, b.last)
		}
		b.post("/apps/"+consent+"/revoke", url.Values{})
		if !strings.Contains(b.last, "Access withdrawn") || s.status(conn.AccessToken) != "revoked" || !s.hasRevoked(conn.ConnectionID) {
			t.Fatalf("revoke at the bank: %.300s", b.last)
		}
		s.waitConsent("bank-a", consent+" Revoked/bank")
		if r := s.call("GET", "/v1/accounts/"+id, nil, withToken(conn.AccessToken)); r.Status != 409 || r.Code() != "connection_revoked" {
			t.Fatalf("account after revoke: %d %s", r.Status, r.Body)
		}

		conn2 := s.connect("adaeze", "bank-a")
		consent = s.consentID(db)
		s.must(s.call("POST", "/v1/connections/remove", nil, withToken(conn2.AccessToken)), 204)
		var status, via string
		if err := db.QueryRow(ctx, `SELECT status, revoked_via FROM ob_consents WHERE id = $1`, consent).Scan(&status, &via); err != nil || status != "Revoked" || via != "aggregator" {
			t.Fatalf("consent at the bank after the partner disconnected: %s %s %v", status, via, err)
		}
		s.waitConsent("bank-a", consent+" Revoked/aggregator")

		conn3 := s.connect("adaeze", "bank-a")
		consent = s.consentID(db)
		if _, err := db.Exec(ctx, `UPDATE ob_consents SET expires_at = '2000-01-01' WHERE id = $1`, consent); err != nil {
			t.Fatal(err)
		}
		s.syncNow()
		if got := s.status(conn3.AccessToken); got != "expired" {
			t.Fatalf("a consent that expired at the bank must expire the connection: %v", got)
		}
		if len(s.eventsOf("connection.status_changed", func(d map[string]any) bool {
			return d["connection_id"] == conn3.ConnectionID && d["status"] == "expired" && d["error_code"] == "reauth_required"
		})) == 0 {
			t.Fatal("no reauth_required webhook after the consent expired")
		}
		s.waitConsent("bank-a", consent+" ReconsentRequired/bank")

		conn4 := s.connect("adaeze", "bank-a")
		consent = s.consentID(db)
		if _, err := db.Exec(ctx, `UPDATE ob_consents SET status = 'Revoked', revoked_via = 'bank' WHERE id = $1`, consent); err != nil {
			t.Fatal(err)
		}
		s.syncNow()
		if got := s.status(conn4.AccessToken); got != "revoked" || !s.hasRevoked(conn4.ConnectionID) {
			t.Fatalf("a consent_revoked answer on a resource call must revoke the connection: %v", got)
		}
	})

	s.run("events need a known bank", func(s *Stack) {
		conn := s.connect("tunde", "bank-a")
		consent := s.consentID(s.bankDB("bank-a"))
		aURL, bURL, widget := s.cfg.Bank("bank-a").APIURL(), s.cfg.Bank("bank-b").APIURL(), s.cfg.WidgetURL()
		event := func(iss, aud, jti string) string {
			return fmt.Sprintf(`{"iss":%q,"aud":%q,"jti":%q,"exp":%d,"consent_id":%q,"status":"Revoked"}`, iss, aud, jti, time.Now().Add(time.Minute).Unix(), consent)
		}
		invalid := `"code":"invalid_request","message":"The event is not valid."`
		for _, r := range []struct {
			name, caller, body string
			status             int
			want               string
		}{
			{"no caller", "", event(aURL, widget, "j1"), http.StatusUnauthorized, `"code":"unauthenticated","message":"Send your ID in the Beerus-Caller header."`},
			{"an unknown bank", "bank-z", event(aURL, widget, "j2"), http.StatusForbidden, `"code":"forbidden","message":"bank-z is not a bank Connect knows."`},
			{"bank-a naming bank-b as issuer", "bank-a", event(bURL, widget, "j3"), http.StatusBadRequest, invalid},
			{"bank-a naming another audience", "bank-a", event(aURL, "http://other.localhost", "j4"), http.StatusBadRequest, invalid},
			{"bank-b reporting a consent of bank-a", "bank-b", event(bURL, widget, "j5"), http.StatusNoContent, ""},
		} {
			req, _ := http.NewRequest(http.MethodPost, widget+"/consent-events", strings.NewReader(r.body))
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
				t.Errorf("%s: %d %s %v, want %d containing %s", r.name, resp.StatusCode, body, err, r.status, r.want)
			}
		}
		if got := s.status(conn.AccessToken); got != "active" {
			t.Errorf("status %v", got)
		}
	})

	s.run("events are kept and retried", func(s *Stack) {
		db := s.bankDB("bank-a")
		conn := s.connect("adaeze", "bank-a")
		consent := s.consentID(db)
		s.whileConnectIsDown(func() {
			b := s.bankSignIn("adaeze")
			b.post("/apps/"+consent+"/revoke", url.Values{})
			waitFor(t, waitLimit, "the revoke event to fail once", func() bool {
				var waiting, attempts int
				err := db.QueryRow(t.Context(), `SELECT count(*), coalesce(max(attempts), 0) FROM ob_events WHERE delivered_at IS NULL AND consent_id = $1 AND status = 'Revoked'`, consent).Scan(&waiting, &attempts)
				return err == nil && waiting == 1 && attempts >= 1
			})
		})
		waitFor(t, waitLimit, "the revoke event to be delivered again", func() bool {
			var left, delivered int
			err := db.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE delivered_at IS NULL), count(*) FILTER (WHERE delivered_at IS NOT NULL) FROM ob_events WHERE consent_id = $1`, consent).Scan(&left, &delivered)
			return err == nil && left == 0 && delivered >= 2
		})
		if got := s.status(conn.AccessToken); got != "revoked" {
			t.Errorf("status after the retried event: %v", got)
		}
	})
}
