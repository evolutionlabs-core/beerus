package connect

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

const pushWait = 5 * time.Second

func stubReply(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": {"application/json"}}}
}

func TestWishGeneration(t *testing.T) {
	down := func(ids ...string) []Fault {
		out := []Fault{}
		for _, id := range ids {
			out = append(out, Fault{ID: id, Kind: FaultDown})
		}
		return out
	}
	base := wishGeneration("wld_1", bankWish{phase: PhaseActive, reason: "ok", downs: down("flt_a", "flt_b")})
	if base <= 0 || base >= 1<<62 {
		t.Fatalf("generation %d is not a positive 62 bit number", base)
	}
	rows := []struct {
		name  string
		world string
		wish  bankWish
		same  bool
	}{
		{"same wish", "wld_1", bankWish{phase: PhaseActive, reason: "ok", downs: down("flt_a", "flt_b")}, true},
		{"faults in another order", "wld_1", bankWish{phase: PhaseActive, reason: "ok", downs: down("flt_b", "flt_a")}, true},
		{"another world", "wld_2", bankWish{phase: PhaseActive, reason: "ok", downs: down("flt_a", "flt_b")}, false},
		{"another phase", "wld_1", bankWish{phase: PhaseSuspended, reason: "ok", downs: down("flt_a", "flt_b")}, false},
		{"another reason", "wld_1", bankWish{phase: PhaseActive, reason: "later", downs: down("flt_a", "flt_b")}, false},
		{"one fault fewer", "wld_1", bankWish{phase: PhaseActive, reason: "ok", downs: down("flt_a")}, false},
		{"no faults", "wld_1", bankWish{phase: PhaseActive, reason: "ok"}, false},
		{"fields run together", "wld_1", bankWish{phase: PhaseActive + "o", reason: "k", downs: down("flt_a", "flt_b")}, false},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			got := wishGeneration(r.world, r.wish)
			if (got == base) != r.same {
				t.Fatalf("generation %d against %d, same is %v", got, base, r.same)
			}
		})
	}
}

func TestHungBankDoesNotDelayAnother(t *testing.T) {
	phases := make(chan phaseRequest, 4)
	transport := roundTrip(func(r *http.Request) (*http.Response, error) {
		if strings.HasPrefix(r.URL.Host, "bank-a.") {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/status":
			return stubReply(200, `{"phase":"active","observed_generation":0}`), nil
		case "GET /v1/faults":
			return stubReply(200, `{"data":[]}`), nil
		case "PUT /v1/phase":
			var body phaseRequest
			raw, _ := io.ReadAll(r.Body)
			if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&body); err != nil {
				return nil, err
			}
			phases <- body
			return stubReply(200, `{}`), nil
		}
		return stubReply(404, `{}`), nil
	})
	a := &App{
		cfg:      &config.Config{Banks: []*config.Bank{{ID: "bank-a", Port: 1}, {ID: "bank-b", Port: 2}}},
		role:     RoleCore,
		st:       newState(),
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		clock:    clock.New(nil),
		peers:    &http.Client{Transport: transport},
		pushWake: make(chan struct{}, 1),
		status:   map[string]*BankStatus{"bank-a": {ID: "bank-a", Phase: PhaseActive}, "bank-b": {ID: "bank-b", Phase: PhaseSuspended, Reason: "Suspended."}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.runPush(ctx)
	}()
	select {
	case got := <-phases:
		want := phaseRequest{Phase: PhaseSuspended, Reason: "Suspended.", WorldID: a.WorldID()}
		want.Generation = wishGeneration(want.WorldID, bankWish{phase: want.Phase, reason: want.Reason})
		if got != want {
			t.Fatalf("bank-b was sent %+v, want %+v", got, want)
		}
	case <-time.After(pushWait):
		t.Fatal("bank-b got no phase while bank-a hung")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(pushWait):
		t.Fatal("runPush did not stop and wait for its goroutines")
	}
}
