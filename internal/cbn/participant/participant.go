package participant

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/cbn"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

const (
	headerTimeout = 10 * time.Second
	readLimit     = 30 * time.Second
	writeLimit    = 60 * time.Second
	idleLimit     = 2 * time.Minute
	shutdownWait  = 5 * time.Second
)

type Options struct {
	Logger      *slog.Logger
	Bind        string
	DatabaseURL string
}

type Participant struct {
	dir   string
	file  config.CBNFile
	log   *slog.Logger
	bind  string
	token string
	clock *clock.Clock
	store *store.Store
	books *cbn.Books

	mu     sync.Mutex
	flight map[string]time.Time
}

func New(ctx context.Context, dir string, o Options) (*Participant, error) {
	f, err := config.LoadCBNFile(dir)
	if err != nil {
		return nil, err
	}
	if o.DatabaseURL == "" {
		return nil, fmt.Errorf("%s is not set; set it to the Postgres URL of the CBN", config.DatabaseURLEnv)
	}
	own := config.CBNDir(dir)
	token, err := config.ReadSecret(filepath.Join(own, config.CBNTokenFile))
	if err != nil {
		return nil, fmt.Errorf("control token: %w", err)
	}
	p := &Participant{dir: dir, file: f, log: o.Logger, clock: clock.New(), bind: o.Bind, token: token, flight: map[string]time.Time{}}
	if p.log == nil {
		p.log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	open := context.WithoutCancel(ctx)
	if p.store, err = store.Open(open, o.DatabaseURL, f.Database); err != nil {
		return nil, fmt.Errorf("open the CBN books: %w", err)
	}
	if p.books, err = cbn.New(open, p.store.Pool, p.clock); err != nil {
		p.store.Close()
		return nil, fmt.Errorf("open the CBN books: %w", err)
	}
	return p, nil
}

func Run(ctx context.Context, dir string, o Options) error {
	p, err := New(ctx, dir, o)
	if err != nil {
		return err
	}
	p.log.Info("cbn running", "port", p.file.Port, "control_port", p.file.ControlPort)
	return p.Serve(ctx)
}

func (p *Participant) Close() {
	p.store.Close()
}
