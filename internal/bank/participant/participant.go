package participant

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/consent"
	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/charges"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/countrypack"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

const (
	flushEvery   = 500 * time.Millisecond
	settleEvery  = 2 * time.Second
	shutdownWait = 5 * time.Second
	ensureLimit  = 30 * time.Second
	healEvery    = 2 * time.Second
	pingLimit    = 5 * time.Second
)

type Options struct {
	Logger      *slog.Logger
	BankID      string
	CoreURL     string
	Bind        string
	DatabaseURL string
}

type Participant struct {
	id     string
	dir    string
	cfg    *config.Config
	fees   *charges.Schedule
	log    *slog.Logger
	clock  *clock.Clock
	link   server.Link
	srv    *server.Server
	sender *consent.Sender

	steps    []server.Step
	attempts int
	retryAt  time.Time

	mu      sync.Mutex
	books   *store.Books
	open    *openbanking.Server
	serving *listeners
}

func New(ctx context.Context, dir string, o Options) (*Participant, error) {
	f, err := config.LoadBankFile(dir, o.BankID)
	if err != nil {
		return nil, err
	}
	if o.DatabaseURL == "" {
		return nil, fmt.Errorf("%s is not set; set it to the Postgres URL of the bank", config.DatabaseURLEnv)
	}
	cfg := f.Config(dir, o.DatabaseURL)
	pack, err := countrypack.Nigeria()
	if err != nil {
		return nil, fmt.Errorf("load country pack: %w", err)
	}
	token, err := config.ReadSecret(filepath.Join(config.BankDir(dir, f.ID), config.BankTokenFile))
	if err != nil {
		return nil, fmt.Errorf("control token: %w", err)
	}
	b := cfg.Banks[0]
	p := &Participant{id: b.ID, dir: dir, cfg: cfg, fees: charges.New(pack.Fees), log: o.Logger, clock: clock.New(), link: server.NewLink(token, o.Bind)}
	if p.log == nil {
		p.log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	coreURL := cmp.Or(o.CoreURL, f.CoreURL)
	p.steps = initialSteps(b)
	p.srv = server.New(server.Config{
		Dir:       dir,
		Bank:      *b,
		Fees:      p.fees,
		NIBSSPort: cfg.Connect.NIBSSPort,
		CBNPort:   cfg.Connect.CBNPort,
		CoreURL:   coreURL,
		Link:      p.link,
		Log:       p.log,
		Clock:     p.clock,
		SetupKey:  cfg.SetupKey,
		Phase:     config.PhaseProvisioning,
		Steps:     p.steps,
	})
	p.sender = &consent.Sender{
		Log:       p.log,
		Clock:     p.clock,
		Dir:       dir,
		WidgetURL: cfg.WidgetURL(),
		CA:        filepath.Join(dir, "banks", p.id, config.BankCAFile),
		Servers:   p.servers,
		Bank:      p.bank,
		Record: func(ctx context.Context, incarnation string, e openbanking.ConsentEvent) error {
			c, err := p.srv.NIBSS()
			if err != nil {
				return err
			}
			return c.RecordConsent(ctx, incarnation, e)
		},
		Report: func(_ string, problems []string) { p.srv.SetProblems(problems) },
	}
	return p, nil
}

func Run(ctx context.Context, dir string, o Options) error {
	p, err := New(ctx, dir, o)
	if err != nil {
		return err
	}
	p.log.Info("bank running", "bank", p.id)
	return p.Serve(ctx)
}

func (p *Participant) Server() *server.Server {
	return p.srv
}

func (p *Participant) Close() {
	p.mu.Lock()
	books := p.books
	p.mu.Unlock()
	if books != nil {
		books.Close()
	}
}

func (p *Participant) bank(id string) *config.Bank {
	if id != p.id {
		return nil
	}
	b := p.srv.Bank()
	return &b
}

func (p *Participant) servers() map[string]*openbanking.Server {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.open == nil {
		return map[string]*openbanking.Server{}
	}
	return map[string]*openbanking.Server{p.id: p.open}
}

func (p *Participant) Serve(ctx context.Context) error {
	defer p.Close()
	if err := p.start(ctx); err != nil {
		return err
	}
	sends := make(chan struct{})
	go func() {
		defer close(sends)
		p.srv.Sender().Run(ctx)
	}()
	checks := make(chan struct{})
	go func() {
		defer close(checks)
		p.checkSettlements(ctx)
	}()
	provisioned := make(chan struct{})
	go func() {
		defer close(provisioned)
		p.provision(ctx)
		p.heal(ctx)
	}()
	tick := time.NewTicker(flushEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			<-provisioned
			shut, stop := context.WithTimeout(context.WithoutCancel(ctx), shutdownWait)
			p.stop(shut)
			stop()
			<-sends
			<-checks
			return nil
		case <-tick.C:
			p.sender.Events(ctx)
			p.sender.Ledger(ctx)
			p.refreshIfStale()
			p.srv.Sweep()
		}
	}
}

func (p *Participant) checkSettlements(ctx context.Context) {
	tick := time.NewTicker(settleEvery)
	defer tick.Stop()
	warned := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		err := p.srv.CheckSettlement(ctx)
		switch {
		case err == nil:
			warned = ""
		case ctx.Err() == nil && warned != err.Error():
			warned = err.Error()
			p.log.Warn("settlement not checked", "bank", p.id, "err", err)
		}
	}
}
