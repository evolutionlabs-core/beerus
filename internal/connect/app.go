package connect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

type App struct {
	mu        sync.Mutex
	cfg       *config.Config
	clock     *clock.Clock
	world     *world.World
	st        *State
	key       config.SigningKey
	ctlToken  string
	log       *slog.Logger
	isDirty   bool
	limiters  map[string]*rate.Limiter
	idemLock  sync.Map
	client    *http.Client
	wake      chan struct{}
	statePath string
	sending   chan struct{}
	swept     time.Time
}

type Options struct {
	Real   func() time.Time
	Logger *slog.Logger
	Client *http.Client
}

func New(cfg *config.Config, o Options) (*App, error) {
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
		cfg:       cfg,
		key:       key,
		ctlToken:  token,
		log:       o.Logger,
		limiters:  map[string]*rate.Limiter{},
		client:    o.Client,
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
	st, err := loadState(a.statePath)
	if err != nil {
		return nil, fmt.Errorf("stub state: %w", err)
	}
	if st == nil {
		st = newState()
		st.Clock = clock.State{Pace: cfg.Pace}
	} else if _, err := os.Stat(a.runningPath()); err == nil {
		a.log.Warn("the last run did not shut down cleanly; starting a new world so partners resync", "old_world", st.WorldID)
		st.WorldID = newID("wld")
	}
	for _, d := range st.Deliveries {
		if d.Status == "sending" {
			d.Status = "pending"
		}
	}
	a.st = st
	a.clock = clock.New(st.Clock, o.Real)
	a.world = world.New(world.Config{Seed: cfg.Seed, Genesis: genesis}, st.World)
	return a, nil
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
	a.isDirty = true
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
	a.isDirty = false
	return saveJSON(a.statePath, a.st)
}

func (a *App) runningPath() string {
	return a.cfg.Path("running")
}

func (a *App) Run(ctx context.Context) {
	if err := os.WriteFile(a.runningPath(), []byte(time.Now().Format(time.RFC3339)), 0o600); err != nil {
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
			if a.isDirty {
				if err := a.saveLocked(); err != nil {
					a.log.Error("save state", "err", err)
				}
			}
			a.mu.Unlock()
		}
	}
}

func (a *App) Step() {
	a.mu.Lock()
	a.runConnections()
	a.runRefreshes()
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
		if real.Sub(rec.CreatedReal) > idemTTL {
			delete(a.st.Idem, k)
			a.idemLock.Delete(k)
		}
	}
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
	return a.cfg.Partner(id)
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
	now := a.clock.Now()
	real := a.clock.Real()
	ids := make([]string, 0, len(a.st.Connections))
	for id := range a.st.Connections {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		c := a.st.Connections[id]
		switch c.Status {
		case "revoked", "expired", "reauth_required", "error":
			continue
		}
		if c.Status == "syncing" && real.Before(c.SyncDueReal) {
			continue
		}
		at := now
		expired := !c.ExpiresAt.IsZero() && !now.Before(c.ExpiresAt)
		if expired {
			at = c.ExpiresAt.Add(-time.Millisecond)
		}
		due := c.Status == "syncing" || c.Status == "institution_down" || expired ||
			real.Sub(c.LastSyncedReal) >= a.cfg.SyncInterval() ||
			at.Sub(c.LastSyncedBiz) >= time.Hour
		if !due {
			continue
		}
		a.syncConnection(c, at)
		if expired && c.Status != "revoked" {
			a.setStatus(c, "expired", "reauth_required")
		}
	}
}

func (a *App) syncConnection(c *Connection, at time.Time) bool {
	if a.world.Down(c.Institution, at) {
		if c.Status != "institution_down" {
			c.StatusBeforeDown = c.Status
			a.setStatus(c, "institution_down", "institution_down")
		}
		c.LastSyncedReal = a.clock.Real()
		return false
	}
	changed := map[string]bool{}
	for _, accID := range c.AccountIDs {
		acct := a.st.Accounts[accID]
		if acct == nil || acct.Removed {
			continue
		}
		res, err := a.fetchAccount(c.PartnerID, acct, at)
		if err != nil {
			a.log.Error("fetch", "account", accID, "err", err)
			a.setStatus(c, "error", "internal_error")
			return false
		}
		if res.changed() {
			changed[accID] = true
		}
		if !acct.HistoryDone {
			acct.HistoryDone = true
			changed[accID] = true
			var eff any
			if acct.Cut {
				eff = acct.From.String()
			}
			a.emit(c.PartnerID, "historical_sync.completed", map[string]any{
				"connection_id":        c.ID,
				"account_id":           accID,
				"start_date":           c.StartDate.String(),
				"effective_start_date": eff,
			})
		}
	}
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

func (a *App) runRefreshes() {
	ids := make([]string, 0)
	for id, r := range a.st.Refreshes {
		if r.Status == "queued" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		r := a.st.Refreshes[id]
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
		case a.world.Down(conn.Institution, a.clock.Now()):
			a.syncConnection(conn, a.clock.Now())
			outcome, code = "failed", "institution_down"
		default:
			if !a.syncConnection(conn, a.clock.Now()) {
				outcome, code = "failed", "internal_error"
			}
		}
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
}

var errNotFound = errors.New("not found")

func (a *App) RevokeAtBank(connID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.st.Connections[connID]
	if !ok {
		return errNotFound
	}
	a.setStatus(c, "revoked", "connection_revoked")
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
