package connect

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/setup"
)

func registryApp(now *time.Time, transport roundTrip) *App {
	return &App{
		role:    RoleBank,
		bankID:  "bank-a",
		coreURL: "http://core.invalid",
		st:      newState(),
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		clock:   clock.New(func() time.Time { return *now }),
		peers:   &http.Client{Transport: transport},
	}
}

func TestRegistryRefreshServesStaleThenFailsClosed(t *testing.T) {
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, clock.Lagos)
	var calls int
	down := true
	a := registryApp(&now, func(r *http.Request) (*http.Response, error) {
		calls++
		if down {
			return stubReply(503, `{}`), nil
		}
		return stubReply(200, `{"data":[{"id":"tpp-1"}]}`), nil
	})
	ctx := context.Background()
	steps := []struct {
		name    string
		advance time.Duration
		down    bool
		calls   int
		failed  bool
	}{
		{"first read with core down", 0, true, 1, true},
		{"core up", 2 * refreshFailFast, false, 2, false},
		{"fresh copy needs no call", registryTTL - time.Second, false, 2, false},
		{"stale copy after the TTL", 2 * time.Second, true, 3, false},
		{"still served inside the stale limit", registryStale - 2*registryTTL, true, 4, false},
		{"fails closed past the stale limit", registryTTL, true, 5, true},
		{"fails fast after a failure", refreshFailFast / 2, false, 5, true},
		{"recovers when core is back", 2 * refreshFailFast, false, 6, false},
	}
	for _, s := range steps {
		now, down = now.Add(s.advance), s.down
		err := a.refreshRegistry(ctx)
		if (err != nil) != s.failed || calls != s.calls {
			t.Fatalf("%s: error %v, %d calls; want failed %v and %d calls", s.name, err, calls, s.failed, s.calls)
		}
	}
	if len(a.st.Registry) != 1 || a.st.Registry["tpp-1"] == nil {
		t.Fatalf("registry %v after recovery", a.st.Registry)
	}
}

func TestSlowCoreFetchIsSharedAndHoldsNoLock(t *testing.T) {
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, clock.Lagos)
	in, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	a := registryApp(&now, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		in <- struct{}{}
		<-release
		return stubReply(200, `{"data":[]}`), nil
	})
	const waiting = 5
	errs := make(chan error, waiting)
	var wg sync.WaitGroup
	for range waiting {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- a.refreshRegistry(context.Background())
		}()
	}
	<-in
	for name, lock := range map[string]*sync.Mutex{"registry": &a.regMu, "directory": &a.dirMu, "app": &a.mu} {
		if !lock.TryLock() {
			t.Fatalf("the %s lock is held across the call to core", name)
		}
		lock.Unlock()
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("%d calls reached core for %d waiting requests", calls.Load(), waiting)
	}
}

func TestDirectoryOffersOnlyBanksThatTakeTransfers(t *testing.T) {
	a := &App{role: RoleBank, bankID: "bank-a", cfg: &config.Config{Banks: []*config.Bank{{ID: "bank-a", Name: "Bank A"}}}}
	for _, in := range []struct{ id, status string }{{"bank-a", "active"}, {"bank-b", "suspended"}, {"bank-c", "terminating"}, {"bank-d", "active"}} {
		a.dirBanks = append(a.dirBanks, directoryBank(nibssInstitution(in.id, in.status)))
	}
	var got []string
	for _, b := range a.openInstitutions() {
		got = append(got, b.ID)
	}
	if len(got) != 3 || got[0] != "bank-a" || got[1] != "bank-b" || got[2] != "bank-d" {
		t.Fatalf("offered %v, want bank-a, bank-b and bank-d without the terminating bank-c", got)
	}
	if a.bankLocked("bank-a") != a.cfg.Banks[0] {
		t.Fatal("a bank's own entry must come from its own config")
	}
}

func TestNIBSSPortDefaultAndConflicts(t *testing.T) {
	ln := setup.LocalConnect{PartnerPort: 7443, WidgetPort: 7444, ControlPort: 7480, HealthPort: 7490}
	bankAt := func(port int) setup.Status {
		return setup.Status{Resources: []setup.Resource{{ID: "bank-a", Allocated: setup.Allocation{Port: port, APIPort: port + 1}}}}
	}
	rows := []struct {
		name string
		ln   setup.LocalConnect
		st   setup.Status
		port int
		err  string
	}{
		{"default", ln, setup.Status{}, defaultNIBSSPort, ""},
		{"set", setup.LocalConnect{PartnerPort: 7443, NIBSSPort: 9000}, setup.Status{}, 9000, ""},
		{"a connect port holds it", setup.LocalConnect{PartnerPort: 7443, HealthPort: defaultNIBSSPort}, setup.Status{}, 0, "NIBSS port 7481 is already used by connect.health_port; set connect.nibss_port in local.yaml to a free port"},
		{"a bank port holds it", ln, bankAt(defaultNIBSSPort), 0, "NIBSS port 7481 is already used by bank-a online banking; set connect.nibss_port in local.yaml to a free port"},
		{"a bank API port holds it", ln, bankAt(defaultNIBSSPort - 1), 0, "NIBSS port 7481 is already used by bank-a open banking API; set connect.nibss_port in local.yaml to a free port"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			port, err := nibssPort(r.ln, r.st)
			if r.err == "" && (err != nil || port != r.port) {
				t.Fatalf("port %d, error %v; want %d", port, err, r.port)
			}
			if r.err != "" && (err == nil || err.Error() != r.err) {
				t.Fatalf("error %v, want %q", err, r.err)
			}
		})
	}
}

func nibssInstitution(id, status string) nibss.Institution {
	return nibss.Institution{ID: id, Name: id, Status: status}
}
