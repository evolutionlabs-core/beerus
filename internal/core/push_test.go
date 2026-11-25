package core

import (
	"context"
	"encoding/json"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/state"
)

const pushWait = 5 * time.Second

func stubReply(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": {"application/json"}}}
}

func bankDirConfig(t *testing.T, banks ...*config.Bank) *config.Config {
	t.Helper()
	cfg := &config.Config{Dir: t.TempDir(), Banks: banks, SetupKey: 1<<63 + 5, Connect: config.Connect{Host: "connect.localhost", WidgetPort: 9000, NIBSSPort: 9001, ControlPort: 9002, BrandName: "Connect"}}
	if err := config.Prepare(cfg); err != nil {
		t.Fatal(err)
	}
	for _, b := range banks {
		b.Database = config.DatabaseName(cfg.Dir, b.ID)
		if err := cfg.EnsureBankDir(b); err != nil {
			t.Fatal(err)
		}
	}
	return cfg
}

func TestWishGeneration(t *testing.T) {
	down := func(ids ...string) []control.Fault {
		out := []control.Fault{}
		for _, id := range ids {
			out = append(out, control.Fault{ID: id, Kind: control.FaultDown})
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
		{"one fault fewer", "wld_1", bankWish{phase: PhaseActive, reason: "ok", downs: down("flt_a")}, false},
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
	phases := make(chan control.PhaseRequest, 4)
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
			var body control.PhaseRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				return nil, err
			}
			phases <- body
			return stubReply(200, `{}`), nil
		}
		return stubReply(404, `{}`), nil
	})
	ctl, err := state.Create(t.TempDir(), setup.Status{})
	if err != nil {
		t.Fatal(err)
	}
	a := &App{
		cfg:      bankDirConfig(t, &config.Bank{ID: "bank-a", Port: 1}, &config.Bank{ID: "bank-b", Port: 2}),
		ctl:      ctl,
		st:       newState(),
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		clock:    clock.New(),
		link:     control.Link{Client: &http.Client{Transport: transport}},
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
		want := control.PhaseRequest{Phase: PhaseSuspended, Reason: "Suspended.", WorldID: a.WorldID()}
		digest, err := a.cfg.BankFileDigest(a.cfg.Bank("bank-b"))
		want.Generation = wishGeneration(want.WorldID, bankWish{phase: want.Phase, reason: want.Reason, config: digest})
		if got != want || err != nil {
			t.Fatalf("bank-b was sent %+v, want %+v (%v)", got, want, err)
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

func TestEnsureBankDirRepairsOnATick(t *testing.T) {
	cfg := bankDirConfig(t, &config.Bank{ID: "bank-a", Name: "Bank A", Port: 1, APIPort: 2, Database: "db"})
	a := &App{cfg: cfg, clock: clock.New()}
	path := config.BankDir(cfg.Dir, "bank-a") + "/" + config.BankFileName
	var intact []byte
	rows := []struct {
		name    string
		elapsed bool
		damage  func() error
		want    bool
	}{
		{name: "first ensure writes", want: true},
		{name: "deleted file inside the interval", damage: func() error { return os.Remove(path) }, want: false},
		{name: "deleted file after the interval", elapsed: true, want: true},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			if r.elapsed {
				last := a.bankEnsures["bank-a"]
				last.at = last.at.Add(-bankEnsureEvery)
				a.bankEnsures["bank-a"] = last
			}
			if r.damage != nil {
				if err := r.damage(); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := a.ensureBankDir("bank-a", false); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if intact == nil {
				intact = got
			}
			if ok := err == nil && string(got) == string(intact); ok != r.want {
				t.Fatalf("bank.json intact is %v, want %v", ok, r.want)
			}
		})
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
