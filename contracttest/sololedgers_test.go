package contracttest

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

const (
	soloJWKX = "f83OJ3D2xF1Bg8vub9tLe1gHMzV76e8Tus9uPHvRVEU"
	soloJWKY = "x_FEzRu9m36HLN_tue659LNpXW6pCyStikYjKIWI5a0"
)

var incarnationText = regexp.MustCompile(`"incarnation":"db[0-9]+"`)

var stampText = regexp.MustCompile(`"(registered_at|changed_at)":"[^"]*"`)

func soloClientBody(name, cn, uri string) string {
	return fmt.Sprintf(`{"name":%q,"legal_name":"Connect Ltd","cac":"RC1","role":"aggregator","cert_cn":%q,"jwk":{"kty":"EC","crv":"P-256","x":%q,"y":%q,"kid":"connect-1"},"redirect_uris":[%q]}`, name, cn, soloJWKX, soloJWKY, uri)
}

func soloClient(id, name, cn, uri string) string {
	return fmt.Sprintf(`{"id":%q,"name":%q,"legal_name":"Connect Ltd","registry_id":%q,"cac":"RC1","role":"aggregator","cert_cn":%q,"jwk":{"kty":"EC","crv":"P-256","x":%q,"y":%q,"kid":"connect-1"},"redirect_uris":[%q],"registered_at":"TIME"}`, id, name, id, cn, soloJWKX, soloJWKY, uri)
}

func soloConsentBody(seq int, id, status string) string {
	return fmt.Sprintf(`{"incarnation":"db1","source_seq":%d,"consent_id":%q,"client_id":"OBR-1","partner_id":"balance","status":%q,"via":"bank","changed_at":"2026-11-14T10:30:00+01:00"}`, seq, id, status)
}

func soloConsent(seq int, bank string, source int, id, status string) string {
	return fmt.Sprintf(`{"seq":%d,"bank":%q,"incarnation":"db1","consent_id":%q,"client_id":"OBR-1","partner_id":"balance","status":%q,"via":"bank","changed_at":"TIME","source_seq":%d}`, seq, bank, id, status, source)
}

func (s *soloNIBSS) registerBank(id, code, nuban, name string) {
	s.t.Helper()
	body := fmt.Sprintf(`{"nip_code":%q,"nuban_code":%q,"name":%q,"type":"commercial","credit_url":"http://%s.localhost:18451","caller_id":%q,"net_debit_cap_minor":500000000,"collateral_minor":0}`, code, nuban, name, id, id)
	if status, got := s.call("nip", s.plain, http.MethodPut, s.control+"/v1/control/banks/"+id, s.token, body); status != http.StatusOK {
		s.t.Fatalf("register %s: %d %s", id, status, got)
	}
}

func TestSoloRegistryAndConsents(t *testing.T) {
	s, banks := startSoloNIBSS(t, soloTSQAfter)
	s.registerBank("bank-a", "000014", "014", "Bank A")
	s.registerBank("bank-b", "000015", "015", "Bank B")
	errBody := func(code, message string) string {
		return fmt.Sprintf(`{"error":{"code":%q,"message":%q,"request_id":"req_ID"}}`, code, message)
	}
	cb := "http://connect.localhost/cb"
	one := soloClientBody("Connect", "beerus-connect", cb)
	oneURL := s.control + "/v1/control/registry/clients/OBR-1"
	twoURL := s.control + "/v1/control/registry/clients/OBR-2"
	list := s.control + "/v1/control/registry/clients"
	bankList := s.dirURL + "/v1/registry/clients"
	both := `{"data":[` + soloClient("OBR-1", "Renamed", "beerus-connect", "https://new.localhost/cb") + `,` + soloClient("OBR-2", "Second", "second", cb) + `]}`
	rows := []struct {
		name   string
		client *http.Client
		method string
		url    string
		token  string
		body   string
		status int
		want   string
	}{
		{"register a client", s.plain, "PUT", oneURL, s.token, one, 200, soloClient("OBR-1", "Connect", "beerus-connect", cb)},
		{"register it again", s.plain, "PUT", oneURL, s.token, one, 200, soloClient("OBR-1", "Connect", "beerus-connect", cb)},
		{"rename it and move the redirect", s.plain, "PUT", oneURL, s.token, soloClientBody("Renamed", "beerus-connect", "https://new.localhost/cb"), 200, soloClient("OBR-1", "Renamed", "beerus-connect", "https://new.localhost/cb")},
		{"another cert_cn", s.plain, "PUT", oneURL, s.token, soloClientBody("Renamed", "other", cb), 422, errBody("registration_conflict", "Client OBR-1 is registered with cert_cn beerus-connect, and this request has cert_cn other. Send the registered details.")},
		{"a name another client holds", s.plain, "PUT", twoURL, s.token, soloClientBody("Second", "beerus-connect", cb), 422, errBody("registration_conflict", "The cert_cn beerus-connect already belongs to client OBR-1. Use another cert_cn.")},
		{"without a token", s.plain, "PUT", oneURL, "", one, 401, errBody("unauthenticated", "A valid control token is required.")},
		{"with a wrong token", s.plain, "PUT", oneURL, "ctl_wrong", one, 401, errBody("unauthenticated", "A valid control token is required.")},
		{"an insecure redirect", s.plain, "PUT", twoURL, s.token, soloClientBody("Second", "second", "http://other.example.com/cb"), 422, errBody("invalid_request", `Redirect URI "http://other.example.com/cb" must be an https URL, or an http URL on localhost.`)},
		{"a bad client id", s.plain, "PUT", s.control + "/v1/control/registry/clients/-x", s.token, one, 422, errBody("invalid_request", `Client id "-x" must be letters, digits, dots, dashes and underscores, up to 40 characters.`)},
		{"an unknown field", s.plain, "PUT", twoURL, s.token, `{"registry_id":"x"}`, 400, errBody("invalid_request", "Send the name, legal_name, cac, role, cert_cn, jwk and redirect_uris of the client.")},
		{"register a second client", s.plain, "PUT", twoURL, s.token, soloClientBody("Second", "second", cb), 200, soloClient("OBR-2", "Second", "second", cb)},
		{"control list", s.plain, "GET", list, s.token, "", 200, both},
		{"control list without a token", s.plain, "GET", list, "", "", 401, errBody("unauthenticated", "A valid control token is required.")},
		{"control first page", s.plain, "GET", list + "?limit=1", s.token, "", 200, `{"data":[` + soloClient("OBR-1", "Renamed", "beerus-connect", "https://new.localhost/cb") + `],"next_cursor":"OBR-1"}`},
		{"control last page", s.plain, "GET", list + "?limit=1&cursor=OBR-1", s.token, "", 200, `{"data":[` + soloClient("OBR-2", "Second", "second", cb) + `]}`},
		{"control bad cursor", s.plain, "GET", list + "?cursor=-bad", s.token, "", 400, errBody("invalid_cursor", "That cursor is not valid. Use the next_cursor value from the last page.")},
		{"control bad limit", s.plain, "GET", list + "?limit=501", s.token, "", 400, errBody("invalid_request", "limit must be a number from 1 to 500.")},
		{"bank list", banks["bank-a"], "GET", bankList, "", "", 200, both},
		{"bank first page", banks["bank-b"], "GET", bankList + "?limit=1", "", "", 200, `{"data":[` + soloClient("OBR-1", "Renamed", "beerus-connect", "https://new.localhost/cb") + `],"next_cursor":"OBR-1"}`},
		{"bank last page", banks["bank-b"], "GET", bankList + "?limit=1&cursor=OBR-1", "", "", 200, `{"data":[` + soloClient("OBR-2", "Second", "second", cb) + `]}`},
		{"bank bad cursor", banks["bank-a"], "GET", bankList + "?cursor=-bad", "", "", 400, errBody("invalid_cursor", "That cursor is not valid. Use the next_cursor value from the last page.")},
		{"bank reads one", banks["bank-a"], "GET", bankList + "/OBR-2", "", "", 200, soloClient("OBR-2", "Second", "second", cb)},
		{"bank reads an unknown one", banks["bank-a"], "GET", bankList + "/OBR-9", "", "", 404, errBody("not_found", "Client OBR-9 is not in the registry.")},
		{"stranger lists", banks["bank-z"], "GET", bankList, "", "", 403, errBody("forbidden", "bank-z may not read the registry. It is not a bank NIBSS knows.")},
		{"stranger reads one", banks["bank-z"], "GET", bankList + "/OBR-1", "", "", 403, errBody("forbidden", "bank-z may not read the registry. It is not a bank NIBSS knows.")},
		{"bank records a consent", banks["bank-a"], "POST", s.dirURL + "/v1/consents", "", soloConsentBody(1, "obc_1", "Authorised"), 200, soloConsent(1, "bank-a", 1, "obc_1", "Authorised")},
		{"another bank, same source seq", banks["bank-b"], "POST", s.dirURL + "/v1/consents", "", soloConsentBody(1, "obc_2", "Authorised"), 200, soloConsent(2, "bank-b", 1, "obc_2", "Authorised")},
		{"the same bank, next source seq", banks["bank-a"], "POST", s.dirURL + "/v1/consents", "", soloConsentBody(2, "obc_1", "Revoked"), 200, soloConsent(3, "bank-a", 2, "obc_1", "Revoked")},
		{"the source seq with another status", banks["bank-a"], "POST", s.dirURL + "/v1/consents", "", soloConsentBody(1, "obc_1", "Revoked"), 422, errBody("idempotency_key_reused", "Source sequence 1 of bank bank-a was first recorded with other status. Send a new source_seq, or repeat the first request.")},
		{"the source seq with another consent and status", banks["bank-a"], "POST", s.dirURL + "/v1/consents", "", soloConsentBody(1, "obc_9", "Revoked"), 422, errBody("idempotency_key_reused", "Source sequence 1 of bank bank-a was first recorded with other consent_id and status. Send a new source_seq, or repeat the first request.")},
		{"the source seq of a new incarnation", banks["bank-a"], "POST", s.dirURL + "/v1/consents", "", strings.Replace(soloConsentBody(1, "obc_3", "Authorised"), "db1", "db2", 1), 200, strings.Replace(soloConsent(4, "bank-a", 1, "obc_3", "Authorised"), "db1", "db2", 1)},
		{"a consent without an incarnation", banks["bank-a"], "POST", s.dirURL + "/v1/consents", "", strings.Replace(soloConsentBody(5, "obc_1", "Revoked"), "db1", "", 1), 422, errBody("invalid_request", "Consent obc_1 event needs an incarnation.")},
		{"a stranger records a consent", banks["bank-z"], "POST", s.dirURL + "/v1/consents", "", soloConsentBody(1, "obc_1", "Authorised"), 403, errBody("forbidden", "bank-z may not record consents. It is not a bank NIBSS knows.")},
		{"a consent that names its bank", banks["bank-a"], "POST", s.dirURL + "/v1/consents", "", `{"bank":"bank-b","source_seq":3}`, 400, errBody("invalid_request", "Send the incarnation, source_seq, consent_id, client_id, partner_id, status, via and changed_at of the consent event.")},
		{"an unknown status", banks["bank-a"], "POST", s.dirURL + "/v1/consents", "", soloConsentBody(3, "obc_1", "Maybe"), 422, errBody("invalid_request", `Consent obc_1 event: status "Maybe" must be AwaitingAuthorisation, Authorised, Rejected, Revoked, Expired, Dormant, ReconsentRequired.`)},
		{"a source seq of zero", banks["bank-a"], "POST", s.dirURL + "/v1/consents", "", soloConsentBody(0, "obc_1", "Revoked"), 422, errBody("invalid_request", "Consent obc_1 event: source seq 0 must be 1 or more.")},
		{"the control ledger", s.plain, "GET", s.control + "/v1/control/consents", s.token, "", 200, `{"data":[` + strings.Replace(soloConsent(4, "bank-a", 1, "obc_3", "Authorised"), "db1", "db2", 1) + `,` + soloConsent(3, "bank-a", 2, "obc_1", "Revoked") + `,` + soloConsent(2, "bank-b", 1, "obc_2", "Authorised") + `,` + soloConsent(1, "bank-a", 1, "obc_1", "Authorised") + `]}`},
		{"the control ledger of one bank", s.plain, "GET", s.control + "/v1/control/consents?bank=bank-a&limit=1", s.token, "", 200, `{"data":[` + strings.Replace(soloConsent(4, "bank-a", 1, "obc_3", "Authorised"), "db1", "db2", 1) + `],"next_cursor":"4"}`},
		{"the next page of one bank", s.plain, "GET", s.control + "/v1/control/consents?bank=bank-a&limit=1&cursor=4", s.token, "", 200, `{"data":[` + soloConsent(3, "bank-a", 2, "obc_1", "Revoked") + `],"next_cursor":"3"}`},
		{"a bank with no events", s.plain, "GET", s.control + "/v1/control/consents?bank=bank-q", s.token, "", 200, `{"data":[]}`},
		{"the ledger without a token", s.plain, "GET", s.control + "/v1/control/consents", "", "", 401, errBody("unauthenticated", "A valid control token is required.")},
		{"the ledger with a bad cursor", s.plain, "GET", s.control + "/v1/control/consents?cursor=abc", s.token, "", 400, errBody("invalid_cursor", "That cursor is not valid. Use the next_cursor value from the last page.")},
		{"the ledger with a bad limit", s.plain, "GET", s.control + "/v1/control/consents?limit=0", s.token, "", 400, errBody("invalid_request", "limit must be a number from 1 to 500.")},
	}
	for _, r := range rows {
		status, got := s.call("nip", r.client, r.method, r.url, r.token, r.body)
		got = stampText.ReplaceAllString(got, `"$1":"TIME"`)
		if status != r.status || got != r.want {
			t.Errorf("%s: got %d %s, want %d %s", r.name, status, got, r.status, r.want)
		}
	}
	status, header, got := s.callWith("nip", s.plain, http.MethodPut, oneURL, s.token, one, http.Header{"If-None-Match": {"*"}})
	if want := errBody("registration_conflict", "Client OBR-1 is already registered. Send another client id."); status != 422 || got != want {
		t.Errorf("create only: got %d %s, want 422 %s", status, got, want)
	}
	replay := soloConsentBody(1, "obc_1", "Authorised")
	status, header, got = s.callWith("nip", banks["bank-a"], http.MethodPost, s.dirURL+"/v1/consents", "", replay, nil)
	got = stampText.ReplaceAllString(got, `"$1":"TIME"`)
	if want := soloConsent(1, "bank-a", 1, "obc_1", "Authorised"); status != 200 || got != want || header.Get("Idempotent-Replayed") != "true" {
		t.Errorf("replay: got %d %s replayed %q, want 200 %s replayed true", status, got, header.Get("Idempotent-Replayed"), want)
	}
	status, header, _ = s.callWith("nip", banks["bank-b"], http.MethodPost, s.dirURL+"/v1/consents", "", soloConsentBody(2, "obc_2", "Revoked"), nil)
	if status != 200 || header.Get("Idempotent-Replayed") != "" {
		t.Errorf("a new event: got %d replayed %q, want 200 and no replay header", status, header.Get("Idempotent-Replayed"))
	}
}

func TestSoloBankFlushesConsentsToNIBSS(t *testing.T) {
	s, _ := startSoloNIBSS(t, soloTSQAfter, "bank-a")
	m := s.members["bank-a"]
	pool := m.books.L.Pool()
	store := &openbanking.PGStore{Pool: func() *pgxpool.Pool { return pool }}
	ctx := context.Background()
	now := time.Now()
	for _, status := range []openbanking.Status{openbanking.Authorised, openbanking.Revoked} {
		e := openbanking.ConsentEvent{ConsentID: "obc_outbox", Bank: "bank-a", ClientID: "OBR-1", PartnerID: "balance", Status: status, Via: "bank", At: now}
		if err := store.EnqueueLedger(ctx, e, now); err != nil {
			t.Fatal(err)
		}
	}
	m.stop()
	s.startBank("bank-a")
	s.registerBank("bank-a", "000014", "014", "Bank A")
	var ledger string
	waitFor(t, waitLimit, "the bank to flush its consents to NIBSS", func() bool {
		status, got := s.call("nip", s.plain, http.MethodGet, s.control+"/v1/control/consents?bank=bank-a", s.token, "")
		ledger = stampText.ReplaceAllString(got, `"$1":"TIME"`)
		return status == http.StatusOK && strings.Contains(ledger, `"status":"Authorised"`) && strings.Contains(ledger, `"status":"Revoked"`)
	})
	ledger = incarnationText.ReplaceAllString(ledger, `"incarnation":"db1"`)
	want := `{"data":[` + soloConsent(2, "bank-a", 2, "obc_outbox", "Revoked") + `,` + soloConsent(1, "bank-a", 1, "obc_outbox", "Authorised") + `]}`
	if ledger != want {
		t.Fatalf("the NIBSS ledger holds %s, want %s", ledger, want)
	}
	var left int
	waitFor(t, waitLimit, "the bank to mark its events delivered", func() bool {
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM ob_events WHERE delivered_at IS NULL`).Scan(&left); err != nil {
			t.Fatal(err)
		}
		return left == 0
	})
	bad := openbanking.ConsentEvent{ConsentID: "obc_bad", Bank: "bank-a", ClientID: "OBR-1", PartnerID: "balance", Status: "Maybe", Via: "bank", At: now}
	good := openbanking.ConsentEvent{ConsentID: "obc_after", Bank: "bank-a", ClientID: "OBR-1", PartnerID: "balance", Status: openbanking.Revoked, Via: "bank", At: now}
	for _, e := range []openbanking.ConsentEvent{bad, good} {
		if err := store.EnqueueLedger(ctx, e, now); err != nil {
			t.Fatal(err)
		}
	}
	token, err := config.ReadSecret(filepath.Join(config.BankDir(s.dir, "bank-a"), config.BankTokenFile))
	if err != nil {
		t.Fatal(err)
	}
	wantProblem := `"problems":["NIBSS refused the Maybe event of consent obc_bad and it will not be sent again: Consent obc_bad event: status \"Maybe\" must be AwaitingAuthorisation, Authorised, Rejected, Revoked, Expired, Dormant, ReconsentRequired.`
	waitFor(t, waitLimit, "the bank to report the refused consent event", func() bool {
		_, got := s.call("", s.plain, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/v1/status", m.bank.Port), token, "")
		return strings.Contains(got, wantProblem)
	})
	waitFor(t, waitLimit, "the event after the refused one to reach NIBSS", func() bool {
		_, got := s.call("nip", s.plain, http.MethodGet, s.control+"/v1/control/consents?bank=bank-a", s.token, "")
		return strings.Contains(got, `"consent_id":"obc_after"`)
	})
}
