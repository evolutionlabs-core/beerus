package connect

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

const (
	RoleSim  = "sim"
	RoleBank = "bank"
)

var bindAddr string

func SetBind(addr string) {
	bindAddr = addr
}

func multiProcess() bool {
	return bindAddr != ""
}

func remotePeers() bool {
	ip := net.ParseIP(bindAddr)
	return multiProcess() && (ip == nil || !ip.IsLoopback())
}

type replica struct {
	WorldID  string            `json:"world_id"`
	Now      time.Time         `json:"business_now"`
	Pace     float64           `json:"pace"`
	World    world.Overrides   `json:"world"`
	Phases   map[string]string `json:"phases"`
	Registry []*RegistryClient `json:"registry"`
}

func (a *App) ctlReplica(w http.ResponseWriter, r *http.Request) {
	rep := replica{Phases: map[string]string{}}
	a.mu.Lock()
	rep.WorldID, rep.Now, rep.Pace = a.st.WorldID, a.clock.Now(), a.clock.State().Pace
	rep.World = a.world.Overrides()
	for _, b := range a.cfg.Banks {
		rep.Phases[b.ID] = a.bankPhase(b.ID)
	}
	a.mu.Unlock()
	rep.Registry = a.RegistryClients()
	writeJSON(w, http.StatusOK, rep)
}

func (a *App) ctlConsentRecord(w http.ResponseWriter, r *http.Request) {
	var e openbanking.ConsentEvent
	if err := decodeBody(r, &e); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Send a consent event.")
		return
	}
	consentLedger{a}.Record(e)
	w.WriteHeader(http.StatusNoContent)
}

const (
	maxPeerBody    = 16 << 20
	resumeAttempts = 5
	peerTimeout    = 30 * time.Second
	replicaTimeout = 5 * time.Second
	replicaRetry   = time.Second
)

var (
	resumeWait = time.Second
	leaseRetry = 10 * time.Second
)

func (a *App) coreDo(method, path string, in, out any) (int, error) {
	return a.peerDo(a.coreURL, method, path, in, out)
}

func (a *App) peerDo(base, method, path string, in, out any) (int, error) {
	return a.peerDoWithin(peerTimeout, base, method, path, in, out)
}

func (a *App) peerDoWithin(limit time.Duration, base, method, path string, in, out any) (int, error) {
	var rd io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(base, "/")+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+a.ctlToken)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.peers.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxPeerBody+1))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("reading the answer to %s: %w", path, err)
	}
	if len(raw) > maxPeerBody {
		return resp.StatusCode, fmt.Errorf("the answer to %s is larger than %d MiB", path, maxPeerBody>>20)
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		msg := e.Error.Message
		if msg == "" {
			msg = resp.Status
		}
		return resp.StatusCode, errors.New(msg)
	}
	if out != nil && len(raw) > 0 {
		return resp.StatusCode, json.Unmarshal(raw, out)
	}
	return resp.StatusCode, nil
}

func newBankRole(cfg *config.Config, o Options) (*App, error) {
	b := cfg.Bank(o.BankID)
	if b == nil {
		return nil, fmt.Errorf("no bank %q in %s", o.BankID, cfg.Path("beerus.yaml"))
	}
	if o.CoreURL == "" {
		return nil, errors.New("a bank needs --core or BEERUS_CORE_URL, the simulator's control URL")
	}
	genesis, err := cfg.GenesisDate()
	if err != nil {
		return nil, err
	}
	token, err := config.ReadSecret(cfg.Path("control-token"))
	if err != nil {
		return nil, fmt.Errorf("control token: %w", err)
	}
	a := &App{
		cfg:      cfg,
		ctlToken: token,
		log:      o.Logger,
		limiters: map[string]*rate.Limiter{},
		peers:    &http.Client{},
		wake:     make(chan struct{}, 1),
		sending:  make(chan struct{}, 4),
		role:     RoleBank,
		bankID:   b.ID,
		coreURL:  o.CoreURL,
	}
	if a.log == nil {
		a.log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	a.st = newState()
	now := time.Now()
	if o.Real != nil {
		now = o.Real()
	}
	a.clock = clock.New(clock.State{AnchorBusiness: now, AnchorReal: now}, o.Real)
	a.world = world.New(a.worldConfig(genesis), world.Overrides{})
	if a.ctl, err = readStore(cfg.Dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if a.ctl == nil {
		a.ctl = &ctlStore{dir: cfg.Dir, readOnly: true}
	}
	a.setupKey = a.ctl.setupKey()
	ls, err := openLedgerSource(context.Background(), cfg, a.ctl, b, a.world)
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

func (a *App) freshen() {
	if a.role != RoleBank {
		return
	}
	a.pullMu.Lock()
	stale := time.Since(a.pulledAt) > 2*time.Millisecond
	a.pullMu.Unlock()
	if stale {
		_ = a.pullReplica()
	}
}

func (a *App) pullReplica() error {
	a.pullMu.Lock()
	if time.Since(a.pulledAt) < 2*time.Millisecond && a.synced.Load() {
		a.pullMu.Unlock()
		return nil
	}
	if a.pullErr != nil && time.Since(a.pullFailed) < replicaRetry {
		err := a.pullErr
		a.pullMu.Unlock()
		return err
	}
	if flight := a.pulling; flight != nil {
		a.pullMu.Unlock()
		<-flight.done
		return flight.err
	}
	flight := &pullFlight{done: make(chan struct{})}
	a.pulling = flight
	a.pullMu.Unlock()
	flight.err = a.fetchReplica()
	a.pullMu.Lock()
	a.pulling = nil
	if flight.err != nil {
		a.pullErr, a.pullFailed = flight.err, time.Now()
	} else {
		a.pullErr = nil
		a.pulledAt = time.Now()
	}
	a.pullMu.Unlock()
	close(flight.done)
	return flight.err
}

type pullFlight struct {
	done chan struct{}
	err  error
}

func (a *App) fetchReplica() error {
	var rep replica
	if _, err := a.peerDoWithin(replicaTimeout, a.coreURL, http.MethodGet, "/internal/replica", nil, &rep); err != nil {
		return err
	}
	sum := sha256.New()
	_ = json.NewEncoder(sum).Encode(rep.World)
	digest := hex.EncodeToString(sum.Sum(nil))
	genesis, err := a.cfg.GenesisDate()
	if err != nil {
		return err
	}
	a.lifeMu.Lock()
	if st := a.status[a.bankID]; st != nil && rep.Phases[a.bankID] != "" {
		st.Phase = rep.Phases[a.bankID]
	}
	a.lifeMu.Unlock()
	a.mu.Lock()
	a.clock.Restore(clock.State{AnchorBusiness: rep.Now})
	_ = a.clock.SetPace(rep.Pace)
	if digest != a.worldSum {
		a.world = world.New(a.worldConfig(genesis), rep.World)
		a.worldSum = digest
		a.log.Info("world updated from the simulator", "world", rep.WorldID)
		for _, l := range a.ledgers {
			if err := l.Keep(a.world); err != nil {
				a.mu.Unlock()
				return err
			}
		}
	}
	reopen := a.st.WorldID != rep.WorldID && a.replicated
	a.st.WorldID = rep.WorldID
	a.regMu.Lock()
	a.st.Registry = map[string]*RegistryClient{}
	for _, c := range rep.Registry {
		a.st.Registry[c.ID] = c
	}
	a.regMu.Unlock()
	a.mu.Unlock()
	if reopen {
		for _, l := range a.ledgers {
			if err := l.Reconnect(); err != nil {
				return err
			}
		}
	}
	a.replicated = true
	a.synced.Store(true)
	return nil
}

func (a *App) ServeBank(ctx context.Context) error {
	b := a.cfg.Bank(a.bankID)
	a.lifeMu.Lock()
	a.serving = true
	a.lifeMu.Unlock()
	if err := a.startBankSite(b); err != nil {
		return err
	}
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	lastErr := ""
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
			if err := a.pullReplica(); err != nil {
				if msg := err.Error(); msg != lastErr {
					a.log.Warn("simulator not reachable", "err", err)
					lastErr = msg
				}
			} else {
				lastErr = ""
			}
		}
	}
}

func (a *App) remoteTransfer(req world.TransferRequest) (world.TransferResult, error) {
	body := transferBody{From: req.From, To: req.To, ToBank: req.ToBank, ToName: req.ToName, Amount: fmt.Sprintf("%d.%02d", req.Amount/100, req.Amount%100), Narration: req.Narration, SessionID: req.SessionID}
	var out struct {
		Result world.TransferResult `json:"result"`
	}
	if _, err := a.coreDo(http.MethodPost, "/transfers", body, &out); err != nil {
		return world.TransferResult{}, err
	}
	return out.Result, nil
}

func (a *App) revokeRemote(b *config.Bank, consent string) error {
	_, err := a.peerDo(b.URL(), http.MethodPost, "/internal/revoke", map[string]string{"consent_id": consent}, nil)
	return err
}

func (a *bankSite) internalRevoke(w http.ResponseWriter, r *http.Request) {
	if !a.peerAuthorised(w, r) {
		return
	}
	var body struct {
		ConsentID string `json:"consent_id"`
	}
	if err := decodeBody(r, &body); err != nil {
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
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(tok), []byte(a.ctlToken)) != 1 {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return false
	}
	return true
}

func (a *bankSite) internalPause(w http.ResponseWriter, r *http.Request) {
	if !a.peerAuthorised(w, r) {
		return
	}
	a.pauseMu.Lock()
	defer a.pauseMu.Unlock()
	if !a.paused.Swap(true) {
		for _, l := range a.ledgers {
			l.Release()
		}
	}
	a.armLease(a.cfg.PauseLease())
	w.WriteHeader(http.StatusNoContent)
}

func (a *bankSite) internalResume(w http.ResponseWriter, r *http.Request) {
	if !a.peerAuthorised(w, r) {
		return
	}
	a.pauseMu.Lock()
	defer a.pauseMu.Unlock()
	if err := a.reconnectLedgers(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.endPause()
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) reconnectLedgers() error {
	for _, l := range a.ledgers {
		if err := l.Reconnect(); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) endPause() {
	if a.pauseTimer != nil {
		a.pauseTimer.Stop()
		a.pauseTimer = nil
	}
	a.paused.Store(false)
}

func (a *App) armLease(d time.Duration) {
	if a.pauseTimer != nil {
		a.pauseTimer.Stop()
	}
	a.pauseTimer = time.AfterFunc(d, a.leaseExpired)
}

func (a *App) leaseExpired() {
	a.pauseMu.Lock()
	defer a.pauseMu.Unlock()
	if !a.paused.Load() {
		return
	}
	if err := a.reconnectLedgers(); err != nil {
		a.log.Warn("pause lease ran out and the bank could not resume; trying again", "err", err)
		a.armLease(leaseRetry)
		return
	}
	a.log.Warn("pause lease ran out without a renewal; the bank resumed on its own")
	a.endPause()
}

func (a *App) pauseBanks() (func() error, error) {
	if a.role != RoleSim {
		return func() error { return nil }, nil
	}
	var targets, paused []*config.Bank
	a.mu.Lock()
	for _, b := range a.cfg.Banks {
		if !b.Closed() && a.ledgerFor(b.ID) != nil {
			targets = append(targets, b)
		}
	}
	a.mu.Unlock()
	for _, b := range targets {
		if _, err := a.peerDo(b.URL(), http.MethodPost, "/internal/pause", nil, nil); err != nil {
			err = fmt.Errorf("%s did not pause: %w", b.Name, err)
			if rerr := a.resumeBanks(paused); rerr != nil {
				err = errors.Join(err, rerr)
			}
			return nil, err
		}
		paused = append(paused, b)
	}
	stop, stopped := make(chan struct{}), make(chan struct{})
	go a.renewPauses(paused, stop, stopped)
	return func() error {
		close(stop)
		<-stopped
		return a.resumeBanks(paused)
	}, nil
}

func (a *App) renewPauses(banks []*config.Bank, stop, stopped chan struct{}) {
	defer close(stopped)
	t := time.NewTicker(a.cfg.PauseLease() / 3)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			for _, b := range banks {
				if _, err := a.peerDo(b.URL(), http.MethodPost, "/internal/pause", nil, nil); err != nil {
					a.log.Warn("pause not renewed; the bank resumes when its lease runs out", "bank", b.ID, "err", err)
				}
			}
		}
	}
}

func (a *App) resumeBanks(banks []*config.Bank) error {
	var errs []error
	for _, b := range banks {
		var err error
		for attempt := 0; attempt < resumeAttempts; attempt++ {
			if attempt > 0 {
				time.Sleep(resumeWait)
			}
			if _, err = a.peerDo(b.URL(), http.MethodPost, "/internal/resume", nil, nil); err == nil {
				break
			}
			a.log.Warn("bank did not resume; trying again", "bank", b.ID, "attempt", attempt+1, "err", err)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s did not resume; it resumes on its own when its pause lease runs out: %w", b.Name, err))
		}
	}
	return errors.Join(errs...)
}
