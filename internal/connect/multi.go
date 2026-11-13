package connect

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

const (
	RoleCore = "core"
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
	WorldID   string            `json:"world_id"`
	Banks     []*config.Bank    `json:"banks"`
	Phases    map[string]string `json:"phases"`
	Directory []nip.Participant `json:"directory"`
	Faults    []Fault           `json:"faults"`
	Registry  []*RegistryClient `json:"registry"`
}

func (a *App) ctlReplica(w http.ResponseWriter, r *http.Request) {
	rep := replica{Phases: map[string]string{}}
	a.mu.Lock()
	ids := []string{}
	for _, b := range a.cfg.Banks {
		ids = append(ids, b.ID)
		c := *b
		rep.Banks = append(rep.Banks, &c)
	}
	rep.WorldID = a.st.WorldID
	rep.Directory = slices.Clone(a.members)
	rep.Faults = a.faultsLocked(a.clock.Now())
	a.mu.Unlock()
	for _, id := range ids {
		rep.Phases[id] = a.bankPhase(id)
	}
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
	peerTimeout    = 30 * time.Second
	replicaTimeout = 5 * time.Second
	replicaRetry   = time.Second
	replicaFresh   = 2 * time.Millisecond
	probeTimeout   = 5 * time.Second
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
		return nil, fmt.Errorf("no bank %q in %s", o.BankID, cfg.Path(statusFile))
	}
	if o.CoreURL == "" {
		return nil, errors.New("a bank needs --core or BEERUS_CORE_URL, the control URL of the core participant")
	}
	token, err := config.ReadSecret(cfg.Path("control-token"))
	if err != nil {
		return nil, fmt.Errorf("control token: %w", err)
	}
	a := &App{
		cfg:       cfg,
		ctlToken:  token,
		log:       o.Logger,
		limiters:  map[string]*rate.Limiter{},
		peers:     &http.Client{Timeout: peerTimeout},
		probeHTTP: &http.Client{Timeout: probeTimeout},
		wake:      make(chan struct{}, 1),
		sending:   make(chan struct{}, 4),
		role:      RoleBank,
		bankID:    b.ID,
		coreURL:   o.CoreURL,
	}
	if a.log == nil {
		a.log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	a.st = newState()
	a.clock = clock.New(o.Real)
	if a.ctl, err = readStore(cfg.Dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if a.ctl == nil {
		a.ctl = &ctlStore{dir: cfg.Dir, readOnly: true}
	}
	a.setupKey = a.ctl.setupKey()
	ls, err := openLedgerSource(context.Background(), cfg, a.ctl, b)
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
	stale := a.clock.Real().Sub(a.pulledAt) > replicaFresh
	a.pullMu.Unlock()
	if !stale {
		return
	}
	err := a.pullReplica()
	a.pullMu.Lock()
	defer a.pullMu.Unlock()
	switch {
	case err == nil:
		a.pullWarned = ""
	case a.pullWarned != err.Error():
		a.pullWarned = err.Error()
		a.log.Warn("replica is stale; serving the last copy", "bank", a.bankID, "err", err)
	}
}

func (a *App) pullReplica() error {
	a.pullMu.Lock()
	if a.clock.Real().Sub(a.pulledAt) < replicaFresh && a.synced.Load() {
		a.pullMu.Unlock()
		return nil
	}
	if a.pullErr != nil && a.clock.Real().Sub(a.pullFailed) < replicaRetry {
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
		a.pullErr, a.pullFailed = flight.err, a.clock.Real()
	} else {
		a.pullErr = nil
		a.pulledAt = a.clock.Real()
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
	a.lifeMu.Lock()
	if st := a.status[a.bankID]; st != nil && rep.Phases[a.bankID] != "" {
		st.Phase = rep.Phases[a.bankID]
	}
	a.lifeMu.Unlock()
	a.mu.Lock()
	a.peerBanks = rep.Banks
	a.members = rep.Directory
	a.st.Faults = nil
	for i := range rep.Faults {
		a.st.Faults = append(a.st.Faults, &rep.Faults[i])
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
					a.log.Warn("core not reachable", "err", err)
					lastErr = msg
				}
			} else {
				lastErr = ""
			}
		}
	}
}

func (a *App) remoteTransfer(req TransferRequest) (TransferResult, error) {
	body := transferBody{From: req.From, To: req.To, ToBank: req.ToBank, Amount: fmt.Sprintf("%d.%02d", req.Amount/100, req.Amount%100), Narration: req.Narration}
	var out TransferResult
	status, err := a.coreDo(http.MethodPost, "/banks/"+url.PathEscape(req.FromBank)+"/transfers", body, &out)
	if err != nil && status >= http.StatusBadRequest && status < http.StatusInternalServerError {
		return out, refused(status, nip.SystemError, "transfer_refused", err)
	}
	return out, err
}

func (a *App) remoteEnquiry(code, nuban string) (string, error) {
	var out nameEnquiry
	q := url.Values{"bank_code": {code}, "account_number": {nuban}}
	if _, err := a.coreDo(http.MethodGet, "/nip/name-enquiry?"+q.Encode(), nil, &out); err != nil {
		return "", err
	}
	if out.Code != nip.OK {
		return "", refused(http.StatusUnprocessableEntity, out.Code, "name_enquiry_failed", errors.New(out.Message))
	}
	return out.AccountName, nil
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
