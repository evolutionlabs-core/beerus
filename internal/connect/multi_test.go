package connect

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

func testBankConfig() *config.Config {
	return &config.Config{Seed: 1, Genesis: "2024-01-01", Connect: config.Connect{ControlPort: 7480}, Banks: []*config.Bank{{ID: "bank-a", Name: "Bank A", Type: "commercial", NIPCode: "999001", NUBANCode: "901", HistoryDays: 30, Port: 7445}}}
}

func TestPausingBanksDoesNotHoldTheAppLock(t *testing.T) {
	cfg := testBankConfig()
	a := &App{cfg: cfg, role: RoleSim, st: newState(), log: slog.New(slog.NewTextHandler(io.Discard, nil)), ledgers: []*ledgerSource{{inst: world.Institution{ID: "bank-a"}}}}
	in, release := make(chan struct{}), make(chan struct{})
	a.peers = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		in <- struct{}{}
		<-release
		return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody, Header: http.Header{}}, nil
	})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.pauseBanks()
	}()
	<-in
	if !a.mu.TryLock() {
		t.Fatal("the app lock is held while a bank is being paused")
	}
	a.mu.Unlock()
	close(release)
	<-done
}

func TestReplicaPullTakesLocksInOrder(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(replica{WorldID: "wld_1", Now: time.Now(), Pace: 1, Phases: map[string]string{"bank-a": PhaseActive}})
	}))
	defer core.Close()
	cfg := testBankConfig()
	now := time.Now()
	a := &App{
		cfg: cfg, role: RoleBank, bankID: "bank-a", coreURL: core.URL, peers: &http.Client{}, st: newState(),
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		clock:  clock.New(clock.State{Pace: 1}, func() time.Time { return now }),
		status: map[string]*BankStatus{"bank-a": {ID: "bank-a", Phase: PhaseProvisioning}},
	}
	genesis, _ := cfg.GenesisDate()
	a.world = world.New(a.worldConfig(genesis), world.Overrides{})
	a.lifeMu.Lock()
	done := make(chan error, 1)
	go func() { done <- a.pullReplicaLocked() }()
	deadline := time.Now().Add(2 * time.Second)
	free := false
	for time.Now().Before(deadline) && !free {
		if free = a.mu.TryLock(); free {
			a.mu.Unlock()
		} else {
			time.Sleep(5 * time.Millisecond)
		}
	}
	a.lifeMu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !free {
		t.Fatal("the pull held the app lock while it waited for the lifecycle lock")
	}
}

func TestSlowSimulatorDoesNotQueueEveryRequest(t *testing.T) {
	var calls int
	var mu sync.Mutex
	a := &App{role: RoleBank, coreURL: "http://sim.invalid", peers: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return nil, io.ErrUnexpectedEOF
	})}}
	for i := 0; i < 5; i++ {
		if err := a.pullReplica(); err == nil {
			t.Fatal("pull from a broken simulator worked")
		}
	}
	if calls != 1 {
		t.Fatalf("%d pulls reached a simulator that just failed", calls)
	}
}

func TestControlHostListInMultiProcessMode(t *testing.T) {
	SetBind("0.0.0.0")
	defer SetBind("")
	a := &App{cfg: testBankConfig(), ctlToken: "ctl_secret", coreURL: "http://sim:7480"}
	h := a.localOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for host, want := range map[string]int{"sim:7480": 204, "127.0.0.1:7480": 204, "localhost:7480": 204, "evil.example:7480": 403, "sim:9999": 403, "sim": 403} {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		req.Host, req.RemoteAddr = host, "10.1.2.3:5555"
		req.Header.Set("Authorization", "Bearer ctl_secret")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Host %q answered %d, want %d", host, rec.Code, want)
		}
	}
}
