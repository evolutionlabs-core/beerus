package core

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pb33f/libopenapi"
	validator "github.com/pb33f/libopenapi-validator"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/control"
)

const (
	participantSpec = "../../api/participant/openapi.yaml"
	participantTok  = "control-token"
	probeLimit      = 10 * time.Second
)

type endpoint struct {
	name, call, body string
	status           int
	text             string
}

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

func serve(t *testing.T, ln net.Listener, h http.Handler) string {
	t.Helper()
	srv := &http.Server{Handler: h, ReadHeaderTimeout: probeLimit}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ln)
	}()
	t.Cleanup(func() {
		_ = srv.Close()
		<-done
	})
	return "http://" + ln.Addr().String()
}

func loopback(t *testing.T, addr string) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

func runEndpoints(t *testing.T, v validator.Validator, base, token string, vars map[string]string, rows []endpoint) {
	t.Helper()
	fill := func(s string) string {
		for k, x := range vars {
			s = strings.ReplaceAll(s, k, x)
		}
		return s
	}
	for _, r := range rows {
		fields := strings.Fields(fill(r.call))
		build := func() *http.Request {
			req, err := http.NewRequest(fields[0], base+fields[1], strings.NewReader(fill(r.body)))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+token)
			for _, f := range fields[2:] {
				if key, ok := strings.CutPrefix(f, "key="); ok {
					req.Header.Set("Idempotency-Key", key)
				}
			}
			return req
		}
		valid := !slices.Contains(fields[2:], "raw")
		if ok, errs := v.ValidateHttpRequest(build()); valid && !ok {
			t.Fatalf("%s: request is outside the spec: %v", r.name, errs)
		}
		resp, err := (&http.Client{Timeout: probeLimit}).Do(build())
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if ok, errs := v.ValidateHttpResponse(build(), &http.Response{StatusCode: resp.StatusCode, Header: resp.Header, Body: io.NopCloser(bytes.NewReader(raw))}); valid && !ok {
			t.Fatalf("%s: response %d %s is outside the spec: %v", r.name, resp.StatusCode, raw, errs)
		}
		body := string(raw)
		replayed := resp.Header.Get("Idempotent-Replayed") == "true"
		if resp.StatusCode != r.status || !(strings.Contains(body, fill(r.text)) || strings.Contains(body, strings.ReplaceAll(fill(r.text), `"`, `\"`))) || replayed != slices.Contains(fields[2:], "replay") {
			t.Errorf("%s: got %d %s (replayed %v), want %d containing %q", r.name, resp.StatusCode, body, replayed, r.status, fill(r.text))
		}
	}
}

func controlContract(t *testing.T, v validator.Validator) {
	cfg := bankDirConfig(t, &config.Bank{ID: "bank-a", Name: "Bank A", Port: 1, APIPort: 2, Database: "db"})
	srv := server.New(server.Config{Dir: cfg.Dir, Bank: *cfg.Bank("bank-a"), Link: control.Link{Token: participantTok}, Log: slog.New(slog.DiscardHandler), Clock: clock.New(), Phase: PhaseActive})
	base := serve(t, loopback(t, "127.0.0.1:0"), srv.API())
	vars := map[string]string{"{first}": string(control.FaultID("flt_one"))}
	runEndpoints(t, v, base, participantTok, vars, []endpoint{
		{"status before a phase", "GET /v1/status", "", 200, `"phase":"active","generation":0`},
		{"phase with a bad body", "PUT /v1/phase raw", `{"phase":"asleep","generation":1,"world_id":"wld_1"}`, 400, "with a phase, a generation and a world ID."},
		{"phase active", "PUT /v1/phase", `{"phase":"active","generation":4,"world_id":"wld_1"}`, 200, `"generation":4,"observed_generation":4`},
	})
	if err := os.Remove(config.BankFilePath(cfg.Dir, "bank-a")); err != nil {
		t.Fatal(err)
	}
	runEndpoints(t, v, base, participantTok, vars, []endpoint{
		{"phase with an unreadable config", "PUT /v1/phase raw", `{"phase":"suspended","generation":9,"world_id":"wld_9"}`, 500, "Fix banks/bank-a/bank.json and send the phase again."},
		{"status after the refused phase", "GET /v1/status", "", 200, `"phase":"active","generation":4`},
	})
	renamed := *cfg.Bank("bank-a")
	renamed.Name, renamed.State = "Bank A Renamed", "suspended"
	if err := cfg.EnsureBankDir(&renamed); err != nil {
		t.Fatal(err)
	}
	runEndpoints(t, v, base, participantTok, vars, []endpoint{
		{"phase with a renamed bank", "PUT /v1/phase", `{"phase":"suspended","generation":6,"world_id":"wld_1"}`, 200, `"phase":"suspended","generation":6`},
		{"no faults", "GET /v1/faults", "", 200, `{"data":[]}`},
		{"down fault", "POST /v1/faults key=flt_one", `{"kind":"down","for":"1h"}`, 201, `"id":"{first}","kind":"down"`},
		{"down fault repeated", "POST /v1/faults key=flt_one replay", `{"kind":"down","for":"1h"}`, 201, `"id":"{first}"`},
		{"key reused", "POST /v1/faults key=flt_one", `{"kind":"timeout","outcome":"credited","count":2}`, 422, "This Idempotency-Key was used with a different request."},
		{"no key", "POST /v1/faults raw", `{"kind":"down","for":"1h"}`, 400, "An Idempotency-Key header of 1 to 255 characters is required on writes."},
		{"down for more than a day", "POST /v1/faults key=flt_long", `{"kind":"down","for":"25h"}`, 422, `is above the limit of 24h`},
		{"timeout fault", "POST /v1/faults key=flt_two", `{"kind":"timeout","outcome":"credited","count":2}`, 201, `"kind":"timeout","outcome":"credited","left":2`},
		{"first page", "GET /v1/faults?limit=1", "", 200, `"next_cursor":"{first}"`},
		{"last page", "GET /v1/faults?limit=1&cursor={first}", "", 200, `"kind":"timeout"`},
		{"unknown cursor", "GET /v1/faults?cursor=flt_nothing", "", 400, "That cursor is not valid. Use the next_cursor value from the last page."},
		{"clear", "DELETE /v1/faults", "", 200, `{"cleared":2}`},
		{"key usable after a clear", "POST /v1/faults key=flt_one", `{"kind":"down","for":"1h"}`, 201, `"kind":"down"`},
		{"phase suspended", "PUT /v1/phase", `{"phase":"suspended","reason":"Suspended by the operator.","generation":5,"world_id":"wld_1"}`, 200, `"reason":"Suspended by the operator."`},
	})
	if stale, b := srv.TakeStale(), srv.Bank(); !stale || b.Name != renamed.Name || !b.Suspended() {
		t.Fatalf("after the reload the bank is %q, suspended %v; want %q, true, with the site marked stale", b.Name, b.Suspended(), renamed.Name)
	}
}
