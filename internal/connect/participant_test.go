package connect

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pb33f/libopenapi"
	validator "github.com/pb33f/libopenapi-validator"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

const (
	participantSpec = "../../api/participant/openapi.yaml"
	participantTok  = "control-token"
	probeLimit      = 10 * time.Second
	unauthenticated = `{"error":{"code":"unauthenticated","message":"A valid control token is required.","request_id":"req_ID"}}`
)

var (
	participantRequestID = regexp.MustCompile(`"request_id":"req_[A-Za-z0-9]+"`)
	participantNow       = time.Date(2026, 11, 14, 10, 0, 0, 0, clock.Lagos)
)

type controlReply struct {
	status int
	body   string
}

func TestParticipantControlContract(t *testing.T) {
	spec, err := os.ReadFile(participantSpec)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := libopenapi.NewDocument(spec)
	if err != nil {
		t.Fatal(err)
	}
	v, errs := validator.NewValidator(doc)
	if len(errs) > 0 {
		t.Fatalf("validator: %v", errs)
	}
	a := &App{
		role: RoleBank, bankID: "bank-a", ctlToken: participantTok, st: newState(),
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		clock:  clock.New(func() time.Time { return participantNow }),
		status: map[string]*BankStatus{"bank-a": {ID: "bank-a", Phase: PhaseActive}},
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: a.participantAPI(), ReadHeaderTimeout: probeLimit}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ln)
	}()
	t.Cleanup(func() {
		_ = srv.Close()
		<-done
	})
	client := &http.Client{Timeout: probeLimit}
	base := "http://" + ln.Addr().String()
	call := func(method, path, key, body, token string, validate bool) controlReply {
		t.Helper()
		build := func() *http.Request {
			req, err := http.NewRequest(method, base+path, strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			if key != "" {
				req.Header.Set("Idempotency-Key", key)
			}
			if body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			return req
		}
		if validate && token != "" {
			if ok, errs := v.ValidateHttpRequest(build()); !ok {
				t.Fatalf("%s %s request: %v", method, path, errs)
			}
		}
		resp, err := client.Do(build())
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if validate {
			vresp := &http.Response{StatusCode: resp.StatusCode, Header: resp.Header, Body: io.NopCloser(bytes.NewReader(raw))}
			if ok, errs := v.ValidateHttpResponse(build(), vresp); !ok {
				t.Fatalf("%s %s response %d %s: %v", method, path, resp.StatusCode, raw, errs)
			}
		}
		return controlReply{resp.StatusCode, participantRequestID.ReplaceAllString(strings.TrimSpace(string(raw)), `"request_id":"req_ID"`)}
	}
	first, second := faultIDFor("flt_one"), faultIDFor("flt_two")
	down := `{"kind":"down","for":"1h"}`
	timeout := `{"kind":"timeout","outcome":"credited","count":2}`
	downFault := fmt.Sprintf(`{"id":%q,"kind":"down","until_at":"2026-11-14T11:00:00+01:00","created_at":"2026-11-14T10:00:00+01:00"}`, first)
	timeoutFault := fmt.Sprintf(`{"id":%q,"kind":"timeout","outcome":"credited","left":2,"created_at":"2026-11-14T10:00:00+01:00"}`, second)
	cursorError := `{"error":{"code":"invalid_cursor","message":"That cursor is not valid. Use the next_cursor value from the last page.","request_id":"req_ID"}}`
	badRequest := `{"error":{"code":"invalid_request","message":"Send {\"kind\":\"down\",\"for\":\"1h\"} or {\"kind\":\"timeout\",\"outcome\":\"credited\"}.","request_id":"req_ID"}}`
	rows := []struct {
		name                    string
		method, path, key, body string
		token                   string
		validate                bool
		status                  int
		want                    string
	}{
		{name: "health before the phase", method: "GET", path: "/v1/health", token: participantTok, validate: true, status: 503, want: `{"error":{"code":"unavailable","message":"The participant is still starting. Try again soon.","request_id":"req_ID"}}`},
		{name: "status before the phase", method: "GET", path: "/v1/status", token: participantTok, validate: true, status: 200, want: `{"participant_id":"bank-a","kind":"bank","phase":"provisioning","reason":"Waiting for the control plane to send the phase.","generation":0,"observed_generation":0,"steps":[]}`},
		{name: "no token", method: "GET", path: "/v1/status", validate: true, status: 401, want: unauthenticated},
		{name: "wrong token", method: "GET", path: "/v1/faults", token: "nope", validate: true, status: 401, want: unauthenticated},
		{name: "phase with a bad body", method: "PUT", path: "/v1/phase", body: `{"phase":"asleep","generation":1,"world_id":"wld_1"}`, token: participantTok, status: 400, want: `{"error":{"code":"invalid_request","message":"Send {\"phase\":\"active\",\"generation\":1,\"world_id\":\"wld_...\"} with a phase, a generation and a world ID.","request_id":"req_ID"}}`},
		{name: "phase active", method: "PUT", path: "/v1/phase", body: `{"phase":"active","generation":4,"world_id":"wld_1"}`, token: participantTok, validate: true, status: 200, want: `{"participant_id":"bank-a","kind":"bank","phase":"active","generation":4,"observed_generation":4,"steps":[]}`},
		{name: "health after the phase", method: "GET", path: "/v1/health", token: participantTok, validate: true, status: 200, want: `{"participant_id":"bank-a","status":"ok","world_id":"wld_1"}`},
		{name: "no faults", method: "GET", path: "/v1/faults", token: participantTok, validate: true, status: 200, want: `{"data":[]}`},
		{name: "down fault", method: "POST", path: "/v1/faults", key: "flt_one", body: down, token: participantTok, validate: true, status: 201, want: downFault},
		{name: "down fault repeated", method: "POST", path: "/v1/faults", key: "flt_one", body: down, token: participantTok, validate: true, status: 201, want: downFault},
		{name: "key reused", method: "POST", path: "/v1/faults", key: "flt_one", body: timeout, token: participantTok, validate: true, status: 422, want: `{"error":{"code":"idempotency_key_reused","message":"Key flt_one was first used with kind down, outcome none, count none and for 1h, and this request has kind timeout, outcome credited, count 2 and for none. Use a new key, or repeat the first request.","request_id":"req_ID"}}`},
		{name: "no key", method: "POST", path: "/v1/faults", body: down, token: participantTok, status: 400, want: `{"error":{"code":"invalid_request","message":"An Idempotency-Key header of 1 to 255 characters is required.","request_id":"req_ID"}}`},
		{name: "unknown field", method: "POST", path: "/v1/faults", key: "flt_bad", body: `{"kind":"down","bank":"bank-b"}`, token: participantTok, status: 400, want: badRequest},
		{name: "down for more than a day", method: "POST", path: "/v1/faults", key: "flt_long", body: `{"kind":"down","for":"25h"}`, token: participantTok, validate: true, status: 422, want: `{"error":{"code":"invalid_request","message":"for \"25h\" is above the limit of 24h","request_id":"req_ID"}}`},
		{name: "timeout without an outcome", method: "POST", path: "/v1/faults", key: "flt_none", body: `{"kind":"timeout"}`, token: participantTok, validate: true, status: 422, want: `{"error":{"code":"invalid_request","message":"outcome \"\" must be credited, failed or unknown","request_id":"req_ID"}}`},
		{name: "timeout fault", method: "POST", path: "/v1/faults", key: "flt_two", body: timeout, token: participantTok, validate: true, status: 201, want: timeoutFault},
		{name: "first page", method: "GET", path: "/v1/faults?limit=1", token: participantTok, validate: true, status: 200, want: fmt.Sprintf(`{"data":[%s],"next_cursor":%q}`, downFault, first)},
		{name: "last page", method: "GET", path: "/v1/faults?limit=1&cursor=" + first, token: participantTok, validate: true, status: 200, want: fmt.Sprintf(`{"data":[%s]}`, timeoutFault)},
		{name: "unknown cursor", method: "GET", path: "/v1/faults?cursor=flt_nothing", token: participantTok, validate: true, status: 400, want: cursorError},
		{name: "limit above the cap", method: "GET", path: "/v1/faults?limit=501", token: participantTok, status: 400, want: `{"error":{"code":"invalid_request","message":"limit must be a number from 1 to 500.","request_id":"req_ID"}}`},
		{name: "clear", method: "DELETE", path: "/v1/faults", token: participantTok, validate: true, status: 200, want: `{"cleared":2}`},
		{name: "clear again", method: "DELETE", path: "/v1/faults", token: participantTok, validate: true, status: 200, want: `{"cleared":0}`},
		{name: "phase suspended", method: "PUT", path: "/v1/phase", body: `{"phase":"suspended","reason":"Suspended by the operator.","generation":5,"world_id":"wld_1"}`, token: participantTok, validate: true, status: 200, want: `{"participant_id":"bank-a","kind":"bank","phase":"suspended","reason":"Suspended by the operator.","generation":5,"observed_generation":5,"steps":[]}`},
		{name: "stop is not served yet", method: "POST", path: "/v1/stop", token: participantTok, status: 404, want: `{"error":{"code":"not_found","message":"A bank does not serve this control call yet.","request_id":"req_ID"}}`},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			got := call(r.method, r.path, r.key, r.body, r.token, r.validate)
			if got.status != r.status || got.body != r.want {
				t.Fatalf("got %d %s, want %d %s", got.status, got.body, r.status, r.want)
			}
		})
	}
}
