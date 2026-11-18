package connect

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/evolutionlabs-core/beerus/internal/bank/participant"
	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	cbnparticipant "github.com/evolutionlabs-core/beerus/internal/cbn/participant"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/fleet"
	nibssparticipant "github.com/evolutionlabs-core/beerus/internal/nibss/participant"
)

const (
	testProvisionLimit = 60 * time.Second
	testProvisionPoll  = 50 * time.Millisecond
	testNodeRetry      = 200 * time.Millisecond
	testBindTries      = 4
	testBind           = "127.0.0.1"
)

type testNode struct {
	name   string
	cancel context.CancelFunc
	exited chan struct{}
	err    error
}

func spawnTestNode(name string, serve func(context.Context) error) *testNode {
	ctx, cancel := context.WithCancel(context.Background())
	n := &testNode{name: name, cancel: cancel, exited: make(chan struct{})}
	go func() {
		defer close(n.exited)
		n.err = serve(ctx)
	}()
	return n
}

func (n *testNode) halt() error {
	n.cancel()
	<-n.exited
	if n.err != nil {
		return fmt.Errorf("serve %s: %w", n.name, n.err)
	}
	return nil
}

type testNodes struct {
	order []*testNode
}

func (ns *testNodes) halt() error {
	var errs []error
	for i := len(ns.order) - 1; i >= 0; i-- {
		errs = append(errs, ns.order[i].halt())
	}
	ns.order = nil
	return errors.Join(errs...)
}

func (ns *testNodes) add(name string, serve func(context.Context) error) *testNode {
	n := spawnTestNode(name, serve)
	ns.order = append(ns.order, n)
	return n
}

type testBanks struct {
	a *App
}

func (b testBanks) Start(id string) (func(), <-chan struct{}) {
	n := spawnTestNode(id, func(ctx context.Context) error {
		for {
			p, err := participant.New(ctx, b.a.cfg.Dir, participant.Options{Real: b.a.clock.Real, Logger: b.a.log, BankID: id, CoreURL: b.a.cfg.ControlURL(), Bind: testBind, DatabaseURL: b.a.cfg.DatabaseURL})
			if err == nil {
				if err = p.Serve(ctx); err == nil {
					return nil
				}
			}
			if ctx.Err() != nil {
				return nil
			}
			retry := time.NewTimer(testNodeRetry)
			select {
			case <-ctx.Done():
				retry.Stop()
				return nil
			case <-retry.C:
			}
		}
	})
	return n.cancel, n.exited
}

func prepareTestCore(t *testing.T, o Options) (*App, *config.Config) {
	t.Helper()
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	cfg := initTestConfig(t, clock.Date{Y: 2026, M: 11, D: 3})
	real := o.Real
	if real == nil {
		real = time.Now
	}
	if err := cfg.EnsureCertificates(real()); err != nil {
		t.Fatal(err)
	}
	if err := PrepareCBN(cfg, real()); err != nil {
		t.Fatal(err)
	}
	a, err := New(cfg, o)
	if err != nil {
		t.Fatal(err)
	}
	return a, cfg
}

func newTestApp(t *testing.T, o Options) *App {
	t.Helper()
	a, _ := prepareTestCore(t, o)
	return a
}

func dropTestDatabases(t *testing.T, cfg *config.Config) {
	t.Helper()
	ctx := context.WithoutCancel(t.Context())
	names, err := store.List(ctx, cfg.DatabaseURL, config.DatabaseName(cfg.Dir, ""))
	if err == nil {
		err = store.DropDatabases(ctx, cfg.DatabaseURL, names)
	}
	if err != nil {
		t.Errorf("drop databases: %v", err)
	}
}

func startTestNodes(t *testing.T, o Options) (*App, *testNodes, error) {
	t.Helper()
	a, cfg := prepareTestCore(t, o)
	conn, err := pgx.Connect(context.Background(), cfg.DatabaseURL)
	if err != nil {
		t.Skipf("no Postgres for app tests (%v); set BEERUS_TEST_DATABASE_URL", err)
	}
	conn.Close(context.Background())
	t.Cleanup(func() { dropTestDatabases(t, cfg) })
	ns := &testNodes{}
	cbn, err := cbnparticipant.New(context.Background(), cfg.Dir, cbnparticipant.Options{Logger: a.log, Real: a.clock.Real, DatabaseURL: cfg.DatabaseURL})
	if err != nil {
		return nil, ns, err
	}
	ns.add("the CBN", cbn.Serve)
	nibss, err := nibssparticipant.New(context.Background(), cfg.Dir, nibssparticipant.Options{Logger: a.log, Real: a.clock.Real, DatabaseURL: cfg.DatabaseURL})
	if err != nil {
		return nil, ns, errors.Join(err, ns.halt())
	}
	ns.add("NIBSS", nibss.Serve)
	tick := time.NewTicker(testProvisionPoll)
	defer tick.Stop()
	upCtx, upStop := context.WithTimeout(context.Background(), testProvisionLimit)
	defer upStop()
	for !participantsAnswer(upCtx, a) {
		select {
		case <-upCtx.Done():
			return nil, ns, errors.Join(upCtx.Err(), ns.halt())
		case <-tick.C:
		}
	}
	return a, ns, nil
}

func startWithNodes(t *testing.T, o Options) (*App, *testNodes) {
	t.Helper()
	if testing.Short() {
		t.Skip("starts the NIBSS and CBN participants; run without -short")
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	for try := 0; ; try++ {
		a, ns, err := startTestNodes(t, o)
		if err == nil {
			return a, ns
		}
		if try == testBindTries {
			t.Fatal(err)
		}
	}
}

func newTestAppWithNIBSS(t *testing.T, o Options) (*App, func()) {
	t.Helper()
	a, ns := startWithNodes(t, o)
	stop := sync.OnceFunc(func() {
		if err := ns.halt(); err != nil {
			t.Errorf("stop the participants: %v", err)
		}
	})
	t.Cleanup(stop)
	if err := a.syncDirectory(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, b := range a.cfg.Banks {
		if err := a.openCBNAccount(t.Context(), b.ID); err != nil {
			t.Fatal(err)
		}
	}
	return a, stop
}

func newTestStack(t *testing.T, o Options) *App {
	t.Helper()
	o.Bind = testBind
	a, ns := startWithNodes(t, o)
	token, err := config.ReadSecret(a.cfg.Path("control-token"))
	if err != nil {
		t.Fatal(err)
	}
	core := ns.add("core", a.Serve)
	f := fleet.New(a.cfg.ControlURL(), token, testBanks{a}, a.log)
	ns.add("the bank fleet", func(ctx context.Context) error {
		f.Run(ctx)
		f.Close()
		return nil
	})
	t.Cleanup(func() {
		if err := ns.halt(); err != nil {
			t.Errorf("stop the stack: %v", err)
		}
	})
	waitCtx, stop := context.WithTimeout(t.Context(), testProvisionLimit)
	defer stop()
	tick := time.NewTicker(testProvisionPoll)
	defer tick.Stop()
	for provisioning(a) {
		select {
		case <-waitCtx.Done():
			t.Fatalf("banks still provisioning: %+v", a.BankStatuses())
		case <-core.exited:
			t.Fatalf("core stopped: %v", core.err)
		case <-tick.C:
		}
	}
	return a
}

func participantsAnswer(ctx context.Context, a *App) bool {
	return answers(ctx, a.nibssCtl.link, a.nibssCtl.base) && answers(ctx, a.cbnCtl.link, a.cbnCtl.base)
}

func answers(ctx context.Context, link server.Link, base string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/health", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+link.Token)
	resp, err := link.Client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func provisioning(a *App) bool {
	return slices.ContainsFunc(a.BankStatuses(), func(st BankStatus) bool { return st.Phase == PhaseProvisioning || st.Phase == PhaseRepairing })
}
