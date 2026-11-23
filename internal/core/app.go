package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"github.com/evolutionlabs-core/beerus/internal/disk"
	"github.com/evolutionlabs-core/beerus/internal/state"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

type App struct {
	applyMu      sync.Mutex
	bankFileMu   sync.Mutex
	bankEnsures  map[string]bankEnsured
	mu           sync.Mutex
	cfg          *config.Config
	clock        *clock.Clock
	st           *State
	ctl          *state.Store
	setupKey     uint64
	log          *slog.Logger
	isDirty      atomic.Bool
	wake         chan struct{}
	statePath    string
	pmu          sync.RWMutex
	partnerMu    sync.Mutex
	swept        time.Time
	nibssCtl     *nibssAdmin
	nibssEnsured time.Time
	cbnCtl       *cbnAdmin
	dirSynced    bool
	dirAttempts  int
	dirNext      time.Time
	ledgerNotes  map[string]string
	stale        map[string]*staleMember
	closeRuns    map[string]*closeRun
	sendReads    map[string]sendRead
	moveReads    map[string]moveRead
	lifeMu       sync.Mutex
	recMu        sync.Mutex
	stepMu       sync.Mutex
	status       map[string]*BankStatus
	serving      bool
	warnings     []string
	driftAt      time.Time
	driftDue     atomic.Bool
	strikes      map[string]int
	confirms     map[string]*bankConfirm
	confirmWG    sync.WaitGroup
	coreURL      string
	pushWake     chan struct{}
	link         control.Link
	agg          control.ConnectControl
	probeHTTP    *http.Client
}

var errNotFound = errors.New("not found")

type Options struct {
	Logger  *slog.Logger
	CoreURL string
	Bind    string
}

func New(cfg *config.Config, o Options) (*App, error) {
	token, err := config.ReadSecret(cfg.Path("control-token"))
	if err != nil {
		return nil, fmt.Errorf("control token: %w", err)
	}
	a := &App{
		cfg:       cfg,
		log:       o.Logger,
		link:      control.NewLink(token, o.Bind),
		pushWake:  make(chan struct{}, 1),
		probeHTTP: &http.Client{Timeout: probeTimeout},
		coreURL:   o.CoreURL,
		wake:      make(chan struct{}, 1),
		statePath: cfg.Path("stub-state.json"),
	}
	if a.log == nil {
		a.log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	if a.ctl, err = state.Open(cfg.Dir); err != nil {
		return nil, err
	}
	a.setupKey = a.ctl.SetupKey()
	if err := state.EnsureConnectState(cfg.Dir); err != nil {
		return nil, err
	}
	st, err := loadState(a.statePath)
	if err != nil {
		return nil, fmt.Errorf("stub state: %w", err)
	}
	if st == nil {
		st = newState()
	} else if _, err := os.Stat(a.runningPath()); err == nil {
		a.log.Warn("the last run did not shut down cleanly; starting a new run so partners resync", "old_run", st.WorldID)
		st.WorldID = web.NewID("wld")
	}
	if d := st.dropped; d.registry+d.consents > 0 {
		a.log.Warn("registry clients and consent history in the old state are dropped; they now live in NIBSS", "registry_clients", d.registry, "consent_events", d.consents)
	}
	a.st = st
	a.clock = clock.New()
	for _, b := range cfg.Banks {
		if _, err := a.ensureBankDir(b.ID, true); err != nil {
			return nil, err
		}
		if err := a.claimBank(b); err != nil {
			return nil, err
		}
	}
	if err := a.prepareNIBSS(); err != nil {
		return nil, err
	}
	if err := a.prepareCBN(); err != nil {
		return nil, err
	}
	if err := a.prepareConnect(); err != nil {
		return nil, err
	}
	a.agg = control.NewConnectControl(a.link, a.connectHost(), cfg.Connect.ConnectControlPort)
	a.initLifecycle()
	return a, nil
}

func (a *App) Config() *config.Config {
	return a.cfg
}

func (a *App) dirty() {
	a.isDirty.Store(true)
}

func (a *App) poke() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func (a *App) Save() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.saveLocked()
}

func (a *App) saveLocked() error {
	a.isDirty.Store(false)
	return saveJSON(a.statePath, a.st)
}

func (a *App) runningPath() string {
	return a.cfg.Path("running")
}

func (a *App) Run(ctx context.Context) {
	if err := disk.Write(a.runningPath(), []byte(a.clock.Real().Format(time.RFC3339)), 0o600); err != nil {
		a.log.Error("write run marker", "err", err)
	}
	defer os.Remove(a.runningPath())
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	save := time.NewTicker(time.Second)
	defer save.Stop()
	for {
		select {
		case <-ctx.Done():
			if err := a.Save(); err != nil {
				a.log.Error("save state", "err", err)
			}
			return
		case <-tick.C:
			a.Step(ctx)
		case <-a.wake:
			a.Step(ctx)
		case <-save.C:
			a.mu.Lock()
			if a.isDirty.Load() {
				if err := a.saveLocked(); err != nil {
					a.log.Error("save state", "err", err)
				}
			}
			a.mu.Unlock()
		}
	}
}

func (a *App) Step(ctx context.Context) {
	a.confirmDue(ctx)
	a.registerDue(ctx)
	a.stepLocked(ctx)
}

func (a *App) stepLocked(ctx context.Context) {
	a.stepMu.Lock()
	defer a.stepMu.Unlock()
	if a.recMu.TryLock() {
		a.reconcileBanks(ctx)
		a.recMu.Unlock()
	}
	if err := a.ensureNIBSSDir(); err != nil {
		a.log.Error("NIBSS directory not written", "err", err)
	}
	a.mu.Lock()
	a.sweep()
	a.mu.Unlock()
}

func (a *App) sweep() {
	real := a.clock.Real()
	if real.Sub(a.swept) < time.Minute {
		return
	}
	a.swept = real
	a.pruneFaultsLocked(a.clock.Now())
	a.dirty()
}

func (a *App) partnerOf(id string) *config.Partner {
	a.pmu.RLock()
	defer a.pmu.RUnlock()
	return a.cfg.Partner(id)
}

func (a *App) connectHost() string {
	if a.link.Remote() {
		return a.cfg.Connect.Host
	}
	return control.Loopback
}
