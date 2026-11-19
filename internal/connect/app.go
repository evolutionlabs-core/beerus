package connect

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/pki"
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
	key          config.SigningKey
	log          *slog.Logger
	isDirty      atomic.Bool
	limiters     map[string]*rate.Limiter
	idem         web.Idempotency
	client       *http.Client
	wake         chan struct{}
	statePath    string
	sending      chan struct{}
	pmu          sync.RWMutex
	partnerMu    sync.Mutex
	swept        time.Time
	nibssCtl     *nibssAdmin
	nibssEnsured time.Time
	cbnCtl       *cbnAdmin
	dirSynced    bool
	dirAttempts  int
	dirNext      time.Time
	regReady     bool
	regProblem   string
	regAttempts  int
	regNext      time.Time
	ledgerNotes  map[string]string
	stale        map[string]*staleMember
	closeRuns    map[string]*closeRun
	sendReads    map[string]sendRead
	moveReads    map[string]moveRead
	revokeReads  map[string]revokeRead
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
	refreshing   map[string]*refreshFlight
	vault        *vault
	obHTTP       *http.Client
	coreURL      string
	pushWake     chan struct{}
	link         control.Link
	probeHTTP    *http.Client
	obKey        *ecdsa.PrivateKey
	bankWrap     func(http.RoundTripper) http.RoundTripper
	eventJTI     jtiSet
}

type Options struct {
	Logger   *slog.Logger
	Client   *http.Client
	CoreURL  string
	Bind     string
	BankWrap func(http.RoundTripper) http.RoundTripper
}

func New(cfg *config.Config, o Options) (*App, error) {
	key, err := config.WebhookKey(cfg)
	if err != nil {
		return nil, err
	}
	token, err := config.ReadSecret(cfg.Path("control-token"))
	if err != nil {
		return nil, fmt.Errorf("control token: %w", err)
	}
	a := &App{
		cfg:       cfg,
		key:       key,
		log:       o.Logger,
		limiters:  map[string]*rate.Limiter{},
		link:      control.NewLink(token, o.Bind),
		pushWake:  make(chan struct{}, 1),
		probeHTTP: &http.Client{Timeout: probeTimeout},
		coreURL:   o.CoreURL,
		client:    o.Client,
		bankWrap:  o.BankWrap,
		wake:      make(chan struct{}, 1),
		statePath: cfg.Path("stub-state.json"),
		sending:   make(chan struct{}, 4),
	}
	if a.log == nil {
		a.log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	if a.client == nil {
		a.client = &http.Client{Timeout: 15 * time.Second}
	}
	if a.ctl, err = state.Open(cfg.Dir); err != nil {
		return nil, err
	}
	a.setupKey = a.ctl.SetupKey()
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
	for _, d := range st.Deliveries {
		if d.Status == "sending" {
			d.Status = "pending"
		}
	}
	a.st = st
	a.clock = clock.New()
	if err := cfg.EnsureCertificates(a.clock.Real()); err != nil {
		return nil, err
	}
	if a.vault, err = loadVault(cfg.Path("connect", "vault.key")); err != nil {
		return nil, err
	}
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
	if err := pki.WriteFile(a.runningPath(), []byte(a.clock.Real().Format(time.RFC3339)), 0o600); err != nil {
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
	a.refreshTokens()
	a.retryRevokes()
	a.runConnections()
	a.runRefreshes()
	a.mu.Lock()
	a.sweep()
	due := a.dueDeliveries()
	a.mu.Unlock()
	for _, d := range due {
		go func(d Delivery) {
			a.sending <- struct{}{}
			defer func() { <-a.sending }()
			a.deliver(d)
		}(d)
	}
}

func (a *App) sweep() {
	real := a.clock.Real()
	if real.Sub(a.swept) < time.Minute {
		return
	}
	a.swept = real
	for k, rec := range a.st.Idem {
		if real.Sub(rec.CreatedReal) > web.IdempotencyTTL {
			delete(a.st.Idem, k)
		}
	}
	a.pruneFaultsLocked(a.clock.Now())
	for k, ls := range a.st.LinkSessions {
		if real.Sub(ls.ExpiresReal) > 7*24*time.Hour {
			delete(a.st.LinkSessions, k)
		}
	}
	for k, ws := range a.st.WidgetSessions {
		if real.After(ws.ExpiresReal) {
			delete(a.st.WidgetSessions, k)
		}
	}
	for k, r := range a.st.Refreshes {
		if r.Status == "done" && real.Sub(r.DoneReal) > 24*time.Hour {
			delete(a.st.Refreshes, k)
		}
	}
	keep := a.st.Deliveries[:0]
	for _, d := range a.st.Deliveries {
		if d.Status == "pending" || d.Status == "sending" || real.Sub(d.CreatedReal) < 7*24*time.Hour {
			keep = append(keep, d)
		}
	}
	a.st.Deliveries = keep
	a.dirty()
}

func (a *App) partnerOf(id string) *config.Partner {
	a.pmu.RLock()
	defer a.pmu.RUnlock()
	return a.cfg.Partner(id)
}

func (a *App) partnersSnapshot() []*config.Partner {
	a.pmu.RLock()
	defer a.pmu.RUnlock()
	return append([]*config.Partner(nil), a.cfg.Partners...)
}

type event struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Data      any    `json:"data"`
}

func (a *App) emit(partner, typ string, data any) {
	body, err := json.Marshal(event{Type: typ, Timestamp: a.clock.Now().Format(time.RFC3339), Data: data})
	if err != nil {
		a.log.Error("marshal webhook", "err", err)
		return
	}
	d := &Delivery{ID: web.NewID("msg"), PartnerID: partner, Type: typ, Body: body, Status: "pending", NextReal: a.clock.Real(), CreatedReal: a.clock.Real()}
	p := a.partnerOf(partner)
	if p == nil || p.WebhookURL == "" {
		d.Status = "no_endpoint"
	}
	a.st.Deliveries = append(a.st.Deliveries, d)
	if len(a.st.Deliveries) > 5000 {
		a.st.Deliveries = a.st.Deliveries[len(a.st.Deliveries)-5000:]
	}
	a.dirty()
}

func (a *App) setStatus(c *Connection, status, code string) {
	if c.Status == status || c.Status == "revoked" {
		return
	}
	prev := c.Status
	c.Status = status
	var ec any
	if code != "" {
		ec = code
	}
	a.emit(c.PartnerID, "connection.status_changed", map[string]any{
		"connection_id":   c.ID,
		"previous_status": prev,
		"status":          status,
		"error_code":      ec,
	})
	a.dirty()
}

func (a *App) runConnections() {
	a.mu.Lock()
	ids := make([]string, 0, len(a.st.Connections))
	for id := range a.st.Connections {
		ids = append(ids, id)
	}
	a.mu.Unlock()
	sort.Strings(ids)
	for _, id := range ids {
		a.runConnection(id)
	}
}

func (a *App) runConnection(id string) {
	a.mu.Lock()
	c := a.st.Connections[id]
	if c == nil {
		a.mu.Unlock()
		return
	}
	switch c.Status {
	case "revoked", "expired", "reauth_required", "error":
		a.mu.Unlock()
		return
	}
	p := planSync(c, a.cfg.SyncInterval(), a.clock.Now(), a.clock.Real())
	a.mu.Unlock()
	if !p.due {
		return
	}
	a.syncConnection(c, p.at)
	if !p.expired {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.st.Connections[id] == c && c.Status != "revoked" {
		a.setStatus(c, "expired", "reauth_required")
	}
}

func (a *App) syncConnection(c *Connection, at time.Time) bool {
	a.mu.Lock()
	if a.st.Connections[c.ID] != c {
		a.mu.Unlock()
		return false
	}
	accIDs := slices.Clone(c.AccountIDs)
	a.mu.Unlock()
	changed := map[string]bool{}
	for _, accID := range accIDs {
		a.mu.Lock()
		acct := a.st.Accounts[accID]
		skip := acct == nil || acct.Removed
		a.mu.Unlock()
		if skip {
			continue
		}
		res, err := a.fetchAccount(c, acct, at)
		a.mu.Lock()
		ok := a.accountSyncedLocked(c, acct, at, res, err, changed)
		a.mu.Unlock()
		if !ok {
			return false
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.st.Connections[c.ID] != c || c.Status == "revoked" {
		return false
	}
	c.FullDue = false
	t := at
	c.LastSyncedAt = &t
	c.LastSyncedReal = a.clock.Real()
	for _, accID := range c.AccountIDs {
		if changed[accID] {
			a.emit(c.PartnerID, "sync.available", map[string]any{"connection_id": c.ID, "account_id": accID, "world_id": a.st.WorldID})
		}
	}
	if c.Status == "syncing" || c.Status == "institution_down" {
		a.setStatus(c, "active", "")
	}
	a.dirty()
	return true
}

func (a *App) accountSyncedLocked(c *Connection, acct *AccountRec, at time.Time, res fetchResult, err error, changed map[string]bool) bool {
	if a.st.Connections[c.ID] != c || c.Status == "revoked" {
		return false
	}
	if err != nil {
		if errors.Is(err, errStale) {
			return false
		}
		if a.bankFailedLocked(c, err) {
			c.LastSyncedReal = a.clock.Real()
			return false
		}
		a.log.Error("fetch", "account", acct.ID, "err", err)
		a.setStatus(c, "error", "internal_error")
		return false
	}
	a.recordAccess(c, acct, at)
	if res.changed() {
		changed[acct.ID] = true
	}
	if !acct.HistoryDone {
		acct.HistoryDone = true
		changed[acct.ID] = true
		var eff any
		if acct.Cut {
			eff = acct.From.String()
		}
		a.emit(c.PartnerID, "historical_sync.completed", map[string]any{
			"connection_id":        c.ID,
			"account_id":           acct.ID,
			"start_date":           c.StartDate.String(),
			"effective_start_date": eff,
		})
	}
	return true
}

func (a *App) recordAccess(c *Connection, acct *AccountRec, at time.Time) {
	for i := len(a.st.Access) - 1; i >= 0; i-- {
		e := a.st.Access[i]
		if e.ConnectionID == c.ID && e.AccountID == acct.ID {
			if at.Sub(e.At) < time.Hour {
				return
			}
			break
		}
	}
	a.st.Access = append(a.st.Access, AccessEvent{ConnectionID: c.ID, PartnerID: c.PartnerID, AccountID: acct.ID, Key: acct.Key, At: at})
	if len(a.st.Access) > 1000 {
		a.st.Access = a.st.Access[len(a.st.Access)-1000:]
	}
}

func (a *App) runRefreshes() {
	a.mu.Lock()
	ids := make([]string, 0)
	for id, r := range a.st.Refreshes {
		if r.Status == "queued" {
			ids = append(ids, id)
		}
	}
	a.mu.Unlock()
	sort.Strings(ids)
	for _, id := range ids {
		a.runRefresh(id)
	}
}

func (a *App) runRefresh(id string) {
	a.mu.Lock()
	r := a.st.Refreshes[id]
	if r == nil || r.Status != "queued" {
		a.mu.Unlock()
		return
	}
	acct := a.st.Accounts[r.AccountID]
	r.Status = "done"
	r.DoneReal = a.clock.Real()
	outcome, code := "succeeded", any(nil)
	var conn *Connection
	if acct != nil {
		if conn, _ = a.grantFor(acct); conn == nil {
			conn = a.st.Connections[acct.ConnectionID]
		}
	}
	switch {
	case acct == nil || conn == nil:
		outcome, code = "failed", "not_found"
	case conn.Status == "revoked":
		outcome, code = "failed", "connection_revoked"
	case conn.Status == "expired" || conn.Status == "reauth_required":
		outcome, code = "failed", "reauth_required"
	default:
		conn.FullDue = true
	}
	if outcome == "succeeded" {
		now := a.clock.Now()
		a.mu.Unlock()
		synced := a.syncConnection(conn, now)
		a.mu.Lock()
		outcome, code = a.refreshOutcomeLocked(conn, synced)
	}
	defer a.mu.Unlock()
	connID := ""
	if conn != nil {
		connID = conn.ID
	}
	partner := ""
	if acct != nil {
		partner = acct.PartnerID
	}
	a.emit(partner, "refresh.completed", map[string]any{
		"refresh_id":    r.ID,
		"connection_id": connID,
		"account_id":    r.AccountID,
		"outcome":       outcome,
		"error_code":    code,
	})
}

func (a *App) refreshOutcomeLocked(conn *Connection, synced bool) (string, any) {
	if synced {
		return "succeeded", nil
	}
	switch conn.Status {
	case "institution_down":
		return "failed", "institution_down"
	case "revoked":
		return "failed", "connection_revoked"
	case "expired", "reauth_required":
		return "failed", "reauth_required"
	}
	return "failed", "internal_error"
}

var errNotFound = errors.New("not found")

func (a *App) RevokeAtBank(ctx context.Context, connID string) error {
	a.mu.Lock()
	c, ok := a.st.Connections[connID]
	if !ok {
		a.mu.Unlock()
		return errNotFound
	}
	bank, consent := c.Institution, c.ConsentID
	a.mu.Unlock()
	b := a.cfg.Bank(bank)
	if b == nil || consent == "" {
		return errNotFound
	}
	return a.revokeRemote(ctx, b, consent)
}
