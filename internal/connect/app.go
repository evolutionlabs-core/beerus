package connect

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/pki"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

type App struct {
	mu           sync.Mutex
	cfg          *config.Config
	clock        *clock.Clock
	world        *world.World
	st           *State
	ctl          *ctlStore
	setupKey     uint64
	key          config.SigningKey
	ctlToken     string
	log          *slog.Logger
	isDirty      atomic.Bool
	limiters     map[string]*rate.Limiter
	idemMu       sync.Mutex
	idemLocks    map[string]*idemEntry
	client       *http.Client
	wake         chan struct{}
	statePath    string
	sending      chan struct{}
	pmu          sync.RWMutex
	partnerMu    sync.Mutex
	swept        time.Time
	ledgers      []*ledgerSource
	rtgs         *rtgs
	rtgsTried    time.Time
	rtgsOpening  bool
	nibss        *nibss.Store
	nibssTried   time.Time
	nibssOpening bool
	members      []nip.Participant
	settling     bool
	settleDone   *sync.Cond
	worldOnly    bool
	personas     []string
	lifeMu       sync.Mutex
	recMu        sync.Mutex
	stepMu       sync.Mutex
	status       map[string]*BankStatus
	sites        map[string]*siteServer
	serving      bool
	warnings     []string
	driftAt      time.Time
	driftDue     atomic.Bool
	strikes      map[string]int
	obMu         sync.Mutex
	obs          map[string]*openbanking.Server
	regMu        sync.Mutex
	poolMu       sync.RWMutex
	pools        map[string]*ledgerSource
	flushMu      sync.Mutex
	vault        *vault
	obHTTP       *http.Client
	role         string
	bankID       string
	coreURL      string
	worldSum     string
	replicated   bool
	synced       atomic.Bool
	paused       atomic.Bool
	pauseMu      sync.Mutex
	pauseTimer   *time.Timer
	pullMu       sync.Mutex
	pulledAt     time.Time
	pullWarned   string
	pullFailed   time.Time
	pullErr      error
	pulling      *pullFlight
	peers        *http.Client
	probeHTTP    *http.Client
	resumeWait   time.Duration
	eventMu      sync.Mutex
	eventHTTP    map[string]*http.Client
	bankWrap     func(http.RoundTripper) http.RoundTripper
	obKey        *ecdsa.PrivateKey
	metas        map[string]bankMeta
	eventJTI     jtiSet
}

type Options struct {
	Real      func() time.Time
	Logger    *slog.Logger
	Client    *http.Client
	WorldOnly bool
	Role      string
	BankID    string
	CoreURL   string
	BankWrap  func(http.RoundTripper) http.RoundTripper
	Personas  []string
}

func New(cfg *config.Config, o Options) (*App, error) {
	if o.Role == RoleBank {
		return newBankRole(cfg, o)
	}
	genesis, err := cfg.GenesisDate()
	if err != nil {
		return nil, err
	}
	key, err := config.WebhookKey(cfg)
	if err != nil {
		return nil, err
	}
	token, err := config.ReadSecret(cfg.Path("control-token"))
	if err != nil {
		return nil, fmt.Errorf("control token: %w", err)
	}
	a := &App{
		cfg:        cfg,
		key:        key,
		ctlToken:   token,
		log:        o.Logger,
		limiters:   map[string]*rate.Limiter{},
		peers:      &http.Client{Timeout: peerTimeout},
		probeHTTP:  &http.Client{Timeout: probeTimeout},
		resumeWait: defaultResume,
		coreURL:    o.CoreURL,
		client:     o.Client,
		bankWrap:   o.BankWrap,
		role:       o.Role,
		wake:       make(chan struct{}, 1),
		statePath:  cfg.Path("stub-state.json"),
		sending:    make(chan struct{}, 4),
	}
	a.settleDone = sync.NewCond(&a.mu)
	if a.log == nil {
		a.log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	if a.client == nil {
		a.client = &http.Client{Timeout: 15 * time.Second}
	}
	if a.ctl, err = openStore(cfg.Dir); err != nil {
		return nil, err
	}
	a.setupKey = a.ctl.setupKey()
	st, err := loadState(a.statePath)
	if err != nil {
		return nil, fmt.Errorf("stub state: %w", err)
	}
	fresh := st == nil
	if fresh {
		st = newState()
		st.Clock = clock.State{Pace: cfg.Pace, AnchorBusiness: cfg.Start}
	} else if _, err := os.Stat(a.runningPath()); err == nil {
		a.log.Warn("the last run did not shut down cleanly; starting a new world so partners resync", "old_world", st.WorldID)
		st.WorldID = newID("wld")
	}
	if st.Calendar != clock.CalendarVersion() {
		if !fresh {
			a.log.Warn("the holiday calendar changed since this setup last ran; past transaction history is regenerated with the new holiday dates", "from", st.Calendar, "to", clock.CalendarVersion())
		}
		st.Calendar = clock.CalendarVersion()
	}
	for _, d := range st.Deliveries {
		if d.Status == "sending" {
			d.Status = "pending"
		}
	}
	a.st = st
	a.clock = clock.New(st.Clock, o.Real)
	a.personas = o.Personas
	a.world = world.New(a.worldConfig(genesis), st.World)
	a.worldOnly = o.WorldOnly
	if err := a.prepareOpenBanking(); err != nil {
		return nil, err
	}
	if a.vault, err = loadVault(cfg.Path("connect", "vault.key")); err != nil {
		return nil, err
	}
	for _, b := range cfg.Banks {
		if a.worldOnly {
			break
		}
		if err := a.claimBank(b); err != nil {
			return nil, err
		}
		ls, err := openLedgerSource(context.Background(), cfg, a.ctl, b, a.world)
		if err != nil {
			a.log.Error("bank ledger not ready; retrying in the background", "bank", b.ID, "err", err)
			continue
		}
		a.addLedger(ls)
	}
	a.nibssStore()
	unregistered := map[string]bool{}
	for _, b := range cfg.Banks {
		if a.worldOnly {
			break
		}
		if err := a.registerMember(b); err != nil {
			a.log.Error("bank not in the NIBSS directory; retrying in the background", "bank", b.ID, "err", err)
			unregistered[b.ID] = true
		}
	}
	for _, l := range a.ledgers {
		a.attachMember(l)
	}
	for _, l := range a.ledgers {
		if err := l.Sync(a.clock.Now()); err != nil {
			a.log.Error("bank ledger sync", "bank", l.inst.ID, "err", err)
		}
	}
	if err := a.ensureRTGS(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	if err := a.settleLocked(a.clock.Now()); err != nil {
		a.log.Error("NIP settlement", "err", err)
	}
	a.mu.Unlock()
	for _, b := range a.initLifecycle(unregistered) {
		a.provision(b)
	}
	return a, nil
}

func (a *App) worldConfig(genesis clock.Date) world.Config {
	wc := world.Config{Seed: a.cfg.Seed, Genesis: genesis, Calendar: a.cfg.Calendar(), Personas: a.personas}
	for _, b := range a.cfg.Banks {
		wc.Institutions = append(wc.Institutions, world.Institution{ID: b.ID, Name: b.Name, Type: b.Type, NIPCode: b.NIPCode, NUBANCode: b.NUBANCode, HistoryDays: b.HistoryDays, StableIDs: b.Stable(), Style: b.Style()})
	}
	return wc
}

func (a *App) addLedger(ls *ledgerSource) {
	a.ledgers = append(a.ledgers, ls)
	a.poolMu.Lock()
	if a.pools == nil {
		a.pools = map[string]*ledgerSource{}
	}
	a.pools[ls.inst.ID] = ls
	a.poolMu.Unlock()
}

func (a *App) obLedger(id string) *ledgerSource {
	a.poolMu.RLock()
	defer a.poolMu.RUnlock()
	return a.pools[id]
}

func (a *App) ledgerFor(institution string) *ledgerSource {
	for _, l := range a.ledgers {
		if l.inst.ID == institution {
			return l
		}
	}
	return nil
}

func (a *App) closeLedgers() {
	for _, l := range a.ledgers {
		l.Close()
	}
	if a.rtgs != nil {
		a.rtgs.Close()
	}
	if a.nibss != nil {
		a.nibss.Close()
	}
}

func (a *App) Clock() *clock.Clock {
	return a.clock
}

func (a *App) World() *world.World {
	return a.world
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
	a.st.Clock = a.clock.State()
	a.st.World = a.world.Overrides()
	a.isDirty.Store(false)
	a.regMu.Lock()
	defer a.regMu.Unlock()
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
			a.Step()
		case <-a.wake:
			a.Step()
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

func (a *App) Step() {
	a.stepMu.Lock()
	defer a.stepMu.Unlock()
	if a.recMu.TryLock() {
		a.reconcileBanks()
		a.recMu.Unlock()
	}
	a.nibssStore()
	if err := a.ensureRTGS(); err != nil {
		a.log.Error("CBN ledger", "err", err)
	}
	a.mu.Lock()
	for _, l := range a.ledgers {
		if err := l.Sync(a.clock.Now()); err != nil {
			a.log.Error("bank ledger sync", "bank", l.inst.ID, "err", err)
			a.driftDue.Store(true)
		}
	}
	if err := a.settleLocked(a.clock.Now()); err != nil {
		a.log.Error("NIP settlement", "err", err)
	}
	a.mu.Unlock()
	a.refreshTokens()
	a.retryRevokes()
	a.runConnections()
	a.runRefreshes()
	a.mu.Lock()
	a.sweep()
	due := a.dueDeliveries()
	a.mu.Unlock()
	a.flushEvents()
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
		if real.Sub(rec.CreatedReal) > idemTTL {
			delete(a.st.Idem, k)
		}
	}
	a.pruneNIPLocked(real)
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
	d := &Delivery{ID: newID("msg"), PartnerID: partner, Type: typ, Body: body, Status: "pending", NextReal: a.clock.Real(), CreatedReal: a.clock.Real()}
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
	now := a.clock.Now()
	real := a.clock.Real()
	if c.Status == "syncing" && real.Before(c.SyncDueReal) {
		a.mu.Unlock()
		return
	}
	at := now
	expired := !c.ExpiresAt.IsZero() && !now.Before(c.ExpiresAt)
	if expired {
		at = c.ExpiresAt.Add(-time.Millisecond)
	}
	due := c.Status == "syncing" || c.Status == "institution_down" || expired ||
		real.Sub(c.LastSyncedReal) >= a.cfg.SyncInterval() ||
		at.Sub(c.LastSyncedBiz) >= time.Hour
	a.mu.Unlock()
	if !due {
		return
	}
	a.syncConnection(c, at)
	if !expired {
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
	if !a.live() && a.world.Down(c.Institution, at) {
		if c.Status != "institution_down" {
			c.StatusBeforeDown = c.Status
			a.setStatus(c, "institution_down", "institution_down")
		}
		c.LastSyncedReal = a.clock.Real()
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
	c.LastSyncedBiz = at
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
		if a.live() && a.bankFailed(c, err) {
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
	a.st.Access = append(a.st.Access, AccessEvent{ConnectionID: c.ID, PartnerID: c.PartnerID, AccountID: acct.ID, WorldKey: acct.WorldKey, At: at})
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
	down := false
	switch {
	case acct == nil || conn == nil:
		outcome, code = "failed", "not_found"
	case conn.Status == "revoked":
		outcome, code = "failed", "connection_revoked"
	case conn.Status == "expired" || conn.Status == "reauth_required":
		outcome, code = "failed", "reauth_required"
	case !a.live() && a.world.Down(conn.Institution, a.clock.Now()):
		down = true
	default:
		conn.FullDue = true
	}
	if conn != nil && outcome == "succeeded" {
		now := a.clock.Now()
		a.mu.Unlock()
		synced := a.syncConnection(conn, now)
		a.mu.Lock()
		outcome, code = a.refreshOutcomeLocked(conn, down, synced)
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

func (a *App) refreshOutcomeLocked(conn *Connection, down, synced bool) (string, any) {
	if down {
		return "failed", "institution_down"
	}
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

func (a *App) RevokeAtBank(connID string) error {
	a.mu.Lock()
	c, ok := a.st.Connections[connID]
	if !ok {
		a.mu.Unlock()
		return errNotFound
	}
	if !a.live() {
		a.setStatus(c, "revoked", "connection_revoked")
		a.mu.Unlock()
		return nil
	}
	bank, consent := c.Institution, c.ConsentID
	a.mu.Unlock()
	if b := a.cfg.Bank(bank); a.role == RoleSim && b != nil && consent != "" {
		if err := a.revokeRemote(b, consent); err != nil {
			return err
		}
		return nil
	}
	ob := a.openBanking(bank)
	if ob == nil || consent == "" {
		return errNotFound
	}
	if err := ob.RevokeByBank(context.Background(), consent, "bank"); err != nil {
		return err
	}
	a.flushEvents()
	return nil
}

func (a *App) ForceStatus(connID, status, code string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.st.Connections[connID]
	if !ok {
		return errNotFound
	}
	a.setStatus(c, status, code)
	return nil
}
