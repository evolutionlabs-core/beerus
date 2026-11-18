package contracttest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/participant"
	cbnparticipant "github.com/evolutionlabs-core/beerus/internal/cbn/participant"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/connect"
	"github.com/evolutionlabs-core/beerus/internal/fleet"
	nibssparticipant "github.com/evolutionlabs-core/beerus/internal/nibss/participant"
)

const (
	nodeStartLimit = 30 * time.Second
	nodePoll       = 20 * time.Millisecond
	nodeRetry      = 200 * time.Millisecond
	nodeBind       = "127.0.0.1"
)

type node struct {
	name   string
	cancel context.CancelFunc
	exited chan struct{}
	err    error
}

func spawn(name string, serve func(context.Context) error) *node {
	ctx, cancel := context.WithCancel(context.Background())
	n := &node{name: name, cancel: cancel, exited: make(chan struct{})}
	go func() {
		defer close(n.exited)
		n.err = serve(ctx)
	}()
	return n
}

func (n *node) halt() error {
	n.cancel()
	<-n.exited
	if n.err != nil {
		return fmt.Errorf("serve %s: %w", n.name, n.err)
	}
	return nil
}

func (n *node) await(answers func(context.Context) bool) error {
	deadline := time.NewTimer(nodeStartLimit)
	defer deadline.Stop()
	tick := time.NewTicker(nodePoll)
	defer tick.Stop()
	for {
		if answers(context.Background()) {
			return nil
		}
		select {
		case <-n.exited:
			return fmt.Errorf("%s stopped before it answered: %v", n.name, n.err)
		case <-deadline.C:
			return fmt.Errorf("%s did not answer within %s", n.name, nodeStartLimit)
		case <-tick.C:
		}
	}
}

func controlAnswers(ctx context.Context, port int, token string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/v1/health", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: time.Second}).Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func cbnAnswers(ctx context.Context, dir string) bool {
	f, err := config.LoadCBNFile(dir)
	if err != nil {
		return false
	}
	token, err := config.ReadSecret(filepath.Join(config.CBNDir(dir), config.CBNTokenFile))
	if err != nil {
		return false
	}
	return controlAnswers(ctx, f.ControlPort, token)
}

func nibssAnswers(ctx context.Context, dir string) bool {
	f, err := config.LoadNIBSSFile(dir)
	if err != nil {
		return false
	}
	token, err := config.ReadSecret(filepath.Join(config.NIBSSDir(dir), config.NIBSSTokenFile))
	if err != nil {
		return false
	}
	return controlAnswers(ctx, f.ControlPort, token)
}

func coreAnswers(ctx context.Context, cfg *config.Config) bool {
	token, err := config.ReadSecret(cfg.Path("control-token"))
	if err != nil {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.ControlURL()+"/health", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: time.Second}).Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

type banks struct {
	dir string
	url string
	db  string
	log *slog.Logger
}

func (b banks) Start(id string) (func(), <-chan struct{}) {
	n := spawn(id, func(ctx context.Context) error {
		warned := ""
		for {
			p, err := participant.New(ctx, b.dir, participant.Options{BankID: id, CoreURL: b.url, Bind: nodeBind, DatabaseURL: b.db})
			if err == nil {
				if err = p.Serve(ctx); err == nil {
					return nil
				}
			}
			if ctx.Err() != nil {
				return nil
			}
			if msg := fmt.Sprint(err); err != nil && msg != warned {
				warned = msg
				b.log.Warn("bank participant not running; retrying", "bank", id, "err", err)
			}
			retry := time.NewTimer(nodeRetry)
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

type nodes struct {
	app   *connect.App
	order []*node
	stop  *node
}

func startNodes(cfg *config.Config, opts connect.Options) (*nodes, error) {
	ns := &nodes{}
	opts.Bind = nodeBind
	if err := cfg.EnsureCertificates(time.Now()); err != nil {
		return ns, err
	}
	if err := connect.PrepareCBN(cfg, time.Now()); err != nil {
		return ns, err
	}
	cbn, err := cbnparticipant.New(context.Background(), cfg.Dir, cbnparticipant.Options{Bind: nodeBind, DatabaseURL: cfg.DatabaseURL})
	if err != nil {
		return ns, err
	}
	ns.order = append(ns.order, spawn("the CBN", cbn.Serve))
	if err := ns.order[0].await(func(ctx context.Context) bool { return cbnAnswers(ctx, cfg.Dir) }); err != nil {
		return ns, errors.Join(err, ns.halt())
	}
	if ns.app, err = connect.New(cfg, opts); err != nil {
		return ns, errors.Join(err, ns.halt())
	}
	core := spawn("core", ns.app.Serve)
	ns.order = append(ns.order, core)
	if err := core.await(func(ctx context.Context) bool { return coreAnswers(ctx, cfg) }); err != nil {
		return ns, errors.Join(err, ns.halt())
	}
	nibss, err := nibssparticipant.New(context.Background(), cfg.Dir, nibssparticipant.Options{Bind: nodeBind, DatabaseURL: cfg.DatabaseURL})
	if err != nil {
		return ns, errors.Join(err, ns.halt())
	}
	n := spawn("NIBSS", nibss.Serve)
	ns.order = append(ns.order, n)
	if err := n.await(func(ctx context.Context) bool { return nibssAnswers(ctx, cfg.Dir) }); err != nil {
		return ns, errors.Join(err, ns.halt())
	}
	token, err := config.ReadSecret(cfg.Path("control-token"))
	if err != nil {
		return ns, errors.Join(err, ns.halt())
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	f := fleet.New(cfg.ControlURL(), token, banks{dir: cfg.Dir, url: cfg.ControlURL(), db: cfg.DatabaseURL, log: log}, log)
	ns.stop = spawn("the bank fleet", func(ctx context.Context) error {
		f.Run(ctx)
		f.Close()
		return nil
	})
	return ns, nil
}

func (ns *nodes) halt() error {
	var errs []error
	if ns.stop != nil {
		errs = append(errs, ns.stop.halt())
		ns.stop = nil
	}
	for i := len(ns.order) - 1; i >= 0; i-- {
		errs = append(errs, ns.order[i].halt())
	}
	ns.order = nil
	return errors.Join(errs...)
}
