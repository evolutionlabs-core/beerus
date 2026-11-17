package connect

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

func testBankConfig() *config.Config {
	return &config.Config{Connect: config.Connect{ControlPort: 7480}, Banks: []*config.Bank{{ID: "bank-a", Name: "Bank A", Type: "commercial", NIPCode: "999001", NUBANCode: "901", HistoryDays: 30, Port: 7445}}}
}

func TestReplicaPullTakesLocksInOrder(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(replica{WorldID: "wld_1", Phases: map[string]string{"bank-a": PhaseActive}})
	}))
	defer core.Close()
	cfg := testBankConfig()
	now := time.Now()
	a := &App{
		cfg: cfg, role: RoleBank, bankID: "bank-a", coreURL: core.URL, peers: &http.Client{}, st: newState(),
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		clock:  clock.New(func() time.Time { return now }),
		status: map[string]*BankStatus{"bank-a": {ID: "bank-a", Phase: PhaseProvisioning}},
	}
	a.lifeMu.Lock()
	done := make(chan error, 1)
	go func() { done <- a.fetchReplica() }()
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

func TestSlowCoreDoesNotQueueEveryRequest(t *testing.T) {
	var calls int
	var mu sync.Mutex
	a := &App{role: RoleBank, clock: clock.New(nil), coreURL: "http://core.invalid", peers: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return nil, io.ErrUnexpectedEOF
	})}}
	for i := 0; i < 5; i++ {
		if err := a.pullReplica(); err == nil {
			t.Fatal("pull from a broken core worked")
		}
	}
	if calls != 1 {
		t.Fatalf("%d pulls reached the core that just failed", calls)
	}
}

func TestControlHostListInMultiProcessMode(t *testing.T) {
	a := &App{bind: "0.0.0.0", cfg: testBankConfig(), ctlToken: "ctl_secret", coreURL: "http://sim:7480"}
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

func TestControlAcceptsOnlyLoopbackPeersWhenBoundToLoopback(t *testing.T) {
	cases := []struct {
		bind, remote string
		want         int
	}{{"127.0.0.1", "10.1.2.3:5555", 403}, {"127.0.0.1", "127.0.0.1:5555", 204}, {"0.0.0.0", "10.1.2.3:5555", 204}}
	for _, c := range cases {
		a := &App{bind: c.bind, cfg: testBankConfig(), ctlToken: "ctl_secret", coreURL: "http://sim:7480"}
		h := a.localOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		req.Host, req.RemoteAddr = "sim:7480", c.remote
		req.Header.Set("Authorization", "Bearer ctl_secret")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("bind %s, peer %s answered %d, want %d", c.bind, c.remote, rec.Code, c.want)
		}
	}
}

func TestSlowPullDoesNotHoldThePullLock(t *testing.T) {
	in, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	a := &App{role: RoleBank, clock: clock.New(nil), coreURL: "http://core.invalid", peers: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		in <- struct{}{}
		<-release
		return nil, io.ErrUnexpectedEOF
	})}}
	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func() { errs <- a.pullReplica() }()
	}
	<-in
	free := false
	for deadline := time.Now().Add(time.Second); !free && time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if free = a.pullMu.TryLock(); free {
			a.pullMu.Unlock()
		}
	}
	if !free {
		t.Fatal("the pull lock is held during the call to the core")
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	for i := 0; i < 3; i++ {
		if err := <-errs; err == nil {
			t.Error("a pull from a broken core worked")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("%d calls reached the core for three waiting requests", calls.Load())
	}
}

func TestPeerAnswerOverTheLimitFailsClearly(t *testing.T) {
	big := bytes.Repeat([]byte(" "), maxPeerBody+1)
	a := &App{coreURL: "http://core.invalid", peers: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(big)), Header: http.Header{}}, nil
	})}}
	var out replica
	if _, err := a.coreDo(http.MethodGet, "/internal/replica", nil, &out); err == nil || !strings.Contains(err.Error(), "larger than 16 MiB") {
		t.Fatalf("an oversized answer: %v", err)
	}
	a.peers = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(iotest.ErrReader(errors.New("connection reset"))), Header: http.Header{}}, nil
	})}
	if _, err := a.coreDo(http.MethodGet, "/internal/replica", nil, &out); err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("a broken answer: %v", err)
	}
}
