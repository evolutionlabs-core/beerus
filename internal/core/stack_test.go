package core

import (
	"context"
	"errors"
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"github.com/evolutionlabs-core/beerus/internal/postgres"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	cbnparticipant "github.com/evolutionlabs-core/beerus/internal/cbn/participant"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	nibssparticipant "github.com/evolutionlabs-core/beerus/internal/nibss/participant"
)

const (
	testProvisionLimit = 60 * time.Second
	testProvisionPoll  = 50 * time.Millisecond
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

func prepareTestCore(t *testing.T, o Options) (*App, *config.Config) {
	t.Helper()
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	cfg := initTestConfig(t, clock.Date{Y: 2026, M: 11, D: 3})
	if err := cfg.EnsureCBNDir(); err != nil {
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
	names, err := postgres.List(ctx, cfg.DatabaseURL, config.DatabaseName(cfg.Dir, ""))
	if err == nil {
		err = postgres.DropDatabases(ctx, cfg.DatabaseURL, names)
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
	cbn, err := cbnparticipant.New(context.Background(), cfg.Dir, cbnparticipant.Options{Logger: a.log, DatabaseURL: cfg.DatabaseURL})
	if err != nil {
		return nil, ns, err
	}
	ns.add("the CBN", cbn.Serve)
	nibss, err := nibssparticipant.New(context.Background(), cfg.Dir, nibssparticipant.Options{Logger: a.log, DatabaseURL: cfg.DatabaseURL})
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

func participantsAnswer(ctx context.Context, a *App) bool {
	return answers(ctx, a.nibssCtl.Link, a.nibssCtl.Base) && answers(ctx, a.cbnCtl.link, a.cbnCtl.base)
}

func answers(ctx context.Context, link control.Link, base string) bool {
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
