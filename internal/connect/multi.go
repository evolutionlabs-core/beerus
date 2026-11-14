package connect

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/time/rate"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/setup"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

const (
	RoleCore = "core"
	RoleBank = "bank"
)

func (a *App) ctlConsentRecord(w http.ResponseWriter, r *http.Request) {
	var e openbanking.ConsentEvent
	if err := web.DecodeBody(r, &e); err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "Send a consent event.")
		return
	}
	consentLedger{a}.Record(e)
	w.WriteHeader(http.StatusNoContent)
}

const (
	probeTimeout   = 5 * time.Second
	bankFlushEvery = 500 * time.Millisecond
)

func NewBank(ctx context.Context, dir string, o Options) (*App, error) {
	f, err := config.LoadBankFile(dir, o.BankID)
	if err != nil {
		return nil, err
	}
	if o.DatabaseURL == "" {
		return nil, fmt.Errorf("%s is not set; set it to the Postgres URL of the bank", config.DatabaseURLEnv)
	}
	cfg, err := f.Config(dir, o.DatabaseURL)
	if err != nil {
		return nil, err
	}
	token, err := config.ReadSecret(filepath.Join(config.BankDir(dir, f.ID), config.BankTokenFile))
	if err != nil {
		return nil, fmt.Errorf("control token: %w", err)
	}
	b := cfg.Banks[0]
	a := &App{
		cfg:       cfg,
		log:       o.Logger,
		limiters:  map[string]*rate.Limiter{},
		link:      server.NewLink(token, o.Bind),
		probeHTTP: &http.Client{Timeout: probeTimeout},
		wake:      make(chan struct{}, 1),
		sending:   make(chan struct{}, 4),
		role:      RoleBank,
		bankID:    b.ID,
		coreURL:   cmp.Or(o.CoreURL, f.CoreURL),
	}
	if a.log == nil {
		a.log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	a.st = newState()
	a.clock = clock.New(o.Real)
	ls, err := openLedgerSource(context.WithoutCancel(ctx), cfg, nil, a.clock, b)
	if err != nil {
		return nil, err
	}
	a.addLedger(ls)
	st := newStatus(b, true)
	steps := make([]server.Step, 0, len(st.Steps))
	for _, s := range st.Steps {
		steps = append(steps, server.Step{ID: s.ID, Name: s.Name, Done: s.Done, Error: s.Error, StartedAt: s.StartedAt, CompletedAt: s.At})
	}
	a.srv = server.New(server.Config{
		Dir:       dir,
		Bank:      *b,
		Books:     ls.Bank(),
		Fees:      cfg.Fees(),
		NIBSSPort: cfg.Connect.NIBSSPort,
		CoreURL:   a.coreURL,
		Link:      a.link,
		Log:       a.log,
		Clock:     a.clock,
		Phase:     st.Phase,
		Steps:     steps,
		Reloaded:  a.bankReloaded,
	})
	a.banking = a.srv.Banking()
	a.sites = map[string]*siteServer{}
	a.strikes = map[string]int{}
	a.obs = map[string]*openbanking.Server{}
	return a, nil
}

func (a *App) bankReloaded(want config.Bank) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.cfg.Bank(want.ID)
	b.Name, b.State, b.NetDebitCap, b.Collateral, b.API = want.Name, want.State, want.NetDebitCap, want.Collateral, want.API
}

func (a *App) restartSiteIfStale() {
	if !a.srv.TakeStale() {
		return
	}
	b := a.cfg.Bank(a.bankID)
	a.detach(b, setup.StepSite)
	if err := a.startBankSite(b); err != nil {
		a.log.Error("bank site not restarted; retrying", "bank", a.bankID, "err", err)
		a.srv.MarkStale()
	}
}

func (a *App) ServeBank(ctx context.Context) error {
	b := a.cfg.Bank(a.bankID)
	a.lifeMu.Lock()
	a.serving = true
	a.lifeMu.Unlock()
	if err := a.startBankSite(b); err != nil {
		return err
	}
	tick := time.NewTicker(bankFlushEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			shut, stop := context.WithTimeout(context.Background(), 5*time.Second)
			a.stopBankSites(shut)
			stop()
			a.closeLedgers()
			return nil
		case <-tick.C:
			a.flushEvents()
			a.flushLedger()
			a.restartSiteIfStale()
			a.srv.Sweep()
		}
	}
}

func (a *App) bankCopy(id string) config.Bank {
	a.mu.Lock()
	defer a.mu.Unlock()
	return *a.cfg.Bank(id)
}

func (a *App) revokeRemote(ctx context.Context, b *config.Bank, consent string) error {
	_, err := a.link.Do(ctx, b.URL(), http.MethodPost, "/internal/revoke", map[string]string{"consent_id": consent}, nil)
	return err
}

func (a *bankSite) internalRevoke(w http.ResponseWriter, r *http.Request) {
	if !a.peerAuthorised(w, r) {
		return
	}
	var body struct {
		ConsentID string `json:"consent_id"`
	}
	if err := web.DecodeBody(r, &body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := a.ob.RevokeByBank(r.Context(), body.ConsentID, "bank"); err != nil {
		http.NotFound(w, r)
		return
	}
	a.flushEvents()
	a.flushLedger()
	w.WriteHeader(http.StatusNoContent)
}

func (a *bankSite) peerAuthorised(w http.ResponseWriter, r *http.Request) bool {
	if !a.link.TokenOK(r) {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return false
	}
	return true
}
