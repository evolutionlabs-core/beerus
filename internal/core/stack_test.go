package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	cbnparticipant "github.com/evolutionlabs-core/beerus/internal/cbn/participant"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/control"
	nibssparticipant "github.com/evolutionlabs-core/beerus/internal/nibss/participant"
	"github.com/evolutionlabs-core/beerus/internal/postgres"
)

const (
	testProvisionLimit = 60 * time.Second
	testProvisionPoll  = 50 * time.Millisecond
	testBindTries      = 4
)

type testNode struct {
	name   string
	cancel context.CancelFunc
	exited chan struct{}
	err    error
}

type testNodes []*testNode

func (ns *testNodes) add(name string, serve func(context.Context) error) {
	ctx, cancel := context.WithCancel(context.Background())
	n := &testNode{name: name, cancel: cancel, exited: make(chan struct{})}
	go func() {
		defer close(n.exited)
		n.err = serve(ctx)
	}()
	*ns = append(*ns, n)
}

func (ns *testNodes) halt() error {
	var errs []error
	for i := len(*ns) - 1; i >= 0; i-- {
		n := (*ns)[i]
		n.cancel()
		<-n.exited
		if n.err != nil {
			errs = append(errs, fmt.Errorf("serve %s: %w", n.name, n.err))
		}
	}
	*ns = nil
	return errors.Join(errs...)
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

func startTestNodes(t *testing.T, o Options) (*App, *testNodes, error) {
	t.Helper()
	a, cfg := prepareTestCore(t, o)
	conn, err := pgx.Connect(context.Background(), cfg.DatabaseURL)
	if err != nil {
		t.Skipf("no Postgres for app tests (%v); set BEERUS_TEST_DATABASE_URL", err)
	}
	conn.Close(context.Background())
	t.Cleanup(func() {
		ctx := context.WithoutCancel(t.Context())
		names, err := postgres.List(ctx, cfg.DatabaseURL, config.DatabaseName(cfg.Dir, ""))
		if err == nil {
			err = postgres.DropDatabases(ctx, cfg.DatabaseURL, names)
		}
		if err != nil {
			t.Errorf("drop databases: %v", err)
		}
	})
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
	for !(answers(upCtx, a.nibssCtl.Link, a.nibssCtl.Base) && answers(upCtx, a.cbnCtl.link, a.cbnCtl.base)) {
		select {
		case <-upCtx.Done():
			return nil, ns, errors.Join(upCtx.Err(), ns.halt())
		case <-tick.C:
		}
	}
	return a, ns, nil
}

func newTestAppWithNIBSS(t *testing.T, o Options) (*App, func()) {
	t.Helper()
	if testing.Short() {
		t.Skip("starts the NIBSS and CBN participants; run without -short")
	}
	var a *App
	var ns *testNodes
	for try := 0; ; try++ {
		var err error
		if a, ns, err = startTestNodes(t, o); err == nil {
			break
		} else if try == testBindTries {
			t.Fatal(err)
		}
	}
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
