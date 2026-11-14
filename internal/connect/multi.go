package connect

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/time/rate"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nip"
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

func (a *App) coreDo(ctx context.Context, method, path string, in, out any) (int, error) {
	return a.link.Do(ctx, a.coreURL, method, path, in, out)
}

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
	a.status = map[string]*BankStatus{b.ID: newStatus(b, true)}
	a.sites = map[string]*siteServer{}
	a.strikes = map[string]int{}
	a.obs = map[string]*openbanking.Server{}
	return a, nil
}

func (a *App) reloadBank() error {
	f, err := config.LoadBankFile(a.cfg.Dir, a.bankID)
	if err != nil {
		return err
	}
	want := f.Bank()
	a.lifeMu.Lock()
	defer a.lifeMu.Unlock()
	b := a.cfg.Bank(a.bankID)
	a.mu.Lock()
	changed := b.Name != want.Name || b.API != want.API
	b.Name, b.State, b.NetDebitCap, b.Collateral, b.API = want.Name, want.State, want.NetDebitCap, want.Collateral, want.API
	a.mu.Unlock()
	if st := a.status[a.bankID]; st != nil {
		st.Name = want.Name
	}
	if changed {
		a.siteStale.Store(true)
	}
	return nil
}

func (a *App) restartSiteIfStale() {
	if !a.siteStale.Swap(false) {
		return
	}
	b := a.cfg.Bank(a.bankID)
	a.detach(b, setup.StepSite)
	if err := a.startBankSite(b); err != nil {
		a.log.Error("bank site not restarted; retrying", "bank", a.bankID, "err", err)
		a.siteStale.Store(true)
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
			a.mu.Lock()
			a.sweep()
			a.mu.Unlock()
		}
	}
}

func (a *App) remoteTransfer(ctx context.Context, req TransferRequest) (TransferResult, error) {
	body := transferBody{From: req.From, To: req.To, ToBank: req.ToBank, Amount: fmt.Sprintf("%d.%02d", req.Amount/100, req.Amount%100), Narration: req.Narration}
	var out TransferResult
	status, err := a.coreDo(ctx, http.MethodPost, "/banks/"+url.PathEscape(req.FromBank)+"/transfers", body, &out)
	var pe *server.PeerError
	if err != nil && !errors.As(err, &pe) {
		return out, a.coreAway(err)
	}
	if err != nil && status >= http.StatusBadRequest && status < http.StatusInternalServerError {
		return out, refused(status, nip.SystemError, "transfer_refused", err)
	}
	return out, err
}

func (a *App) coreAway(err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	return &unavailableError{msg: a.bankCopy(a.bankID).Name + " cannot reach NIBSS right now. Try again in a minute.", err: err}
}

func (a *App) bankCopy(id string) config.Bank {
	a.mu.Lock()
	defer a.mu.Unlock()
	return *a.cfg.Bank(id)
}

func (a *App) remoteEnquiry(ctx context.Context, code, nuban string) (string, error) {
	var out nameEnquiry
	q := url.Values{"bank_code": {code}, "account_number": {nuban}}
	_, err := a.coreDo(ctx, http.MethodGet, "/nip/name-enquiry?"+q.Encode(), nil, &out)
	var pe *server.PeerError
	if err != nil && !errors.As(err, &pe) {
		return "", a.coreAway(err)
	}
	if err != nil {
		return "", err
	}
	if out.Code != nip.OK {
		return "", refused(http.StatusUnprocessableEntity, out.Code, "name_enquiry_failed", errors.New(out.Message))
	}
	return out.AccountName, nil
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
