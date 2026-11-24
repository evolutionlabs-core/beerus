package core

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/control"
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

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

const (
	participantSpec = "../../api/participant/openapi.yaml"
	participantTok  = "control-token"
	probeLimit      = 10 * time.Second
	unauthenticated = `{"error":{"code":"unauthenticated","message":"A valid control token is required.","request_id":"req_ID"}}`
)

var (
	participantRequestID = regexp.MustCompile(`"request_id":"req_[A-Za-z0-9]+"`)
	participantStamp     = regexp.MustCompile(`"(created_at|until_at)":"[^"]*"`)
	participantNow       = clock.New().Now()
)

type controlReply struct {
	status   int
	body     string
	replayed bool
}

type contractCall func(method, path, key, body, token string, validate bool) controlReply

func participantValidator() (validator.Validator, error) {
	spec, err := os.ReadFile(participantSpec)
	if err != nil {
		return nil, err
	}
	doc, err := libopenapi.NewDocument(spec)
	if err != nil {
		return nil, err
	}
	v, errs := validator.NewValidator(doc)
	return v, errors.Join(errs...)
}

func newContractCall(t *testing.T, v validator.Validator, handler http.Handler) contractCall {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: probeLimit}
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
	return func(method, path, key, body, token string, validate bool) controlReply {
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
		return controlReply{resp.StatusCode, participantStamp.ReplaceAllString(participantRequestID.ReplaceAllString(strings.TrimSpace(string(raw)), `"request_id":"req_ID"`), `"$1":"T"`), resp.Header.Get("Idempotent-Replayed") == "true"}
	}
}

func controlContract(t *testing.T, v validator.Validator) {
	cfg := bankDirConfig(t, &config.Bank{ID: "bank-a", Name: "Bank A", Port: 1, APIPort: 2, Database: "db"})
	srv := server.New(server.Config{
		Dir:   cfg.Dir,
		Bank:  *cfg.Bank("bank-a"),
		Link:  control.Link{Token: participantTok},
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clock: clock.New(),
		Phase: PhaseActive,
	})
	call := newContractCall(t, v, srv.API())
	bankFile := config.BankFilePath(cfg.Dir, "bank-a")
	renamed := *cfg.Bank("bank-a")
	renamed.Name, renamed.State = "Bank A Renamed", "suspended"
	first, second := control.FaultID("flt_one"), control.FaultID("flt_two")
	down := `{"kind":"down","for":"1h"}`
	timeout := `{"kind":"timeout","outcome":"credited","count":2}`
	downFault := fmt.Sprintf(`{"id":%q,"kind":"down","until_at":"T","created_at":"T"}`, first)
	timeoutFault := fmt.Sprintf(`{"id":%q,"kind":"timeout","outcome":"credited","left":2,"created_at":"T"}`, second)
	cursorError := `{"error":{"code":"invalid_cursor","message":"That cursor is not valid. Use the next_cursor value from the last page.","request_id":"req_ID"}}`
	badRequest := `{"error":{"code":"invalid_request","message":"Send {\"kind\":\"down\",\"for\":\"1h\"} or {\"kind\":\"timeout\",\"outcome\":\"credited\"}.","request_id":"req_ID"}}`
	rows := []struct {
		name                    string
		before                  func()
		method, path, key, body string
		token                   string
		validate                bool
		status                  int
		want                    string
	}{
		{name: "health before the phase", method: "GET", path: "/v1/health", token: participantTok, validate: true, status: 200, want: fmt.Sprintf(`{"participant_id":"bank-a","status":"ok","world_id":%q}`, srv.WorldID())},
		{name: "status before the phase", method: "GET", path: "/v1/status", token: participantTok, validate: true, status: 200, want: `{"participant_id":"bank-a","kind":"bank","phase":"active","generation":0,"observed_generation":0,"steps":[]}`},
		{name: "no token", method: "GET", path: "/v1/status", validate: true, status: 401, want: unauthenticated},
		{name: "wrong token", method: "GET", path: "/v1/faults", token: "nope", validate: true, status: 401, want: unauthenticated},
		{name: "phase with a bad body", method: "PUT", path: "/v1/phase", body: `{"phase":"asleep","generation":1,"world_id":"wld_1"}`, token: participantTok, status: 400, want: `{"error":{"code":"invalid_request","message":"Send {\"phase\":\"active\",\"generation\":1,\"world_id\":\"wld_...\"} with a phase, a generation and a world ID.","request_id":"req_ID"}}`},
		{name: "phase active", method: "PUT", path: "/v1/phase", body: `{"phase":"active","generation":4,"world_id":"wld_1"}`, token: participantTok, validate: true, status: 200, want: `{"participant_id":"bank-a","kind":"bank","phase":"active","generation":4,"observed_generation":4,"steps":[]}`},
		{name: "phase with an unreadable config", before: func() { _ = os.Remove(bankFile) }, method: "PUT", path: "/v1/phase", body: `{"phase":"suspended","generation":9,"world_id":"wld_9"}`, token: participantTok, status: 500, want: `{"error":{"code":"config_unreadable","message":"The bank config could not be read. Fix banks/bank-a/bank.json and send the phase again.","request_id":"req_ID"}}`},
		{name: "status after the refused phase", method: "GET", path: "/v1/status", token: participantTok, validate: true, status: 200, want: `{"participant_id":"bank-a","kind":"bank","phase":"active","generation":4,"observed_generation":4,"steps":[]}`},
		{name: "phase with a renamed bank", before: func() {
			if err := cfg.EnsureBankDir(&renamed); err != nil {
				t.Fatal(err)
			}
		}, method: "PUT", path: "/v1/phase", body: `{"phase":"suspended","generation":6,"world_id":"wld_1"}`, token: participantTok, validate: true, status: 200, want: `{"participant_id":"bank-a","kind":"bank","phase":"suspended","generation":6,"observed_generation":6,"steps":[]}`},
		{name: "phase active again", method: "PUT", path: "/v1/phase", body: `{"phase":"active","generation":7,"world_id":"wld_1"}`, token: participantTok, validate: true, status: 200, want: `{"participant_id":"bank-a","kind":"bank","phase":"active","generation":7,"observed_generation":7,"steps":[]}`},
		{name: "health after the phase", method: "GET", path: "/v1/health", token: participantTok, validate: true, status: 200, want: `{"participant_id":"bank-a","status":"ok","world_id":"wld_1"}`},
		{name: "no faults", method: "GET", path: "/v1/faults", token: participantTok, validate: true, status: 200, want: `{"data":[]}`},
		{name: "down fault", method: "POST", path: "/v1/faults", key: "flt_one", body: down, token: participantTok, validate: true, status: 201, want: downFault},
		{name: "down fault repeated", method: "POST", path: "/v1/faults", key: "flt_one", body: down, token: participantTok, validate: true, status: 201, want: downFault},
		{name: "key reused", method: "POST", path: "/v1/faults", key: "flt_one", body: timeout, token: participantTok, validate: true, status: 422, want: `{"error":{"code":"idempotency_key_reused","message":"This Idempotency-Key was used with a different request.","request_id":"req_ID"}}`},
		{name: "no key", method: "POST", path: "/v1/faults", body: down, token: participantTok, status: 400, want: `{"error":{"code":"invalid_request","message":"An Idempotency-Key header of 1 to 255 characters is required on writes.","request_id":"req_ID"}}`},
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
		{name: "key usable after a clear", method: "POST", path: "/v1/faults", key: "flt_one", body: down, token: participantTok, validate: true, status: 201, want: downFault},
		{name: "phase suspended", method: "PUT", path: "/v1/phase", body: `{"phase":"suspended","reason":"Suspended by the operator.","generation":5,"world_id":"wld_1"}`, token: participantTok, validate: true, status: 200, want: `{"participant_id":"bank-a","kind":"bank","phase":"suspended","reason":"Suspended by the operator.","generation":5,"observed_generation":5,"steps":[]}`},
		{name: "stop is not served yet", method: "POST", path: "/v1/stop", token: participantTok, status: 404, want: `{"error":{"code":"not_found","message":"A bank does not serve this control call yet.","request_id":"req_ID"}}`},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			if r.before != nil {
				r.before()
			}
			got := call(r.method, r.path, r.key, r.body, r.token, r.validate)
			if got.status != r.status || got.body != r.want {
				t.Fatalf("got %d %s, want %d %s", got.status, got.body, r.status, r.want)
			}
		})
	}
	if stale, b := srv.TakeStale(), srv.Bank(); !stale || b.Name != renamed.Name || !b.Suspended() {
		t.Fatalf("after the reload the bank is %q, suspended %v; want %q, true, with the site marked stale", b.Name, b.Suspended(), renamed.Name)
	}
}
