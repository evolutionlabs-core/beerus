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

func (a *App) multiProcess() bool {
	return a.bind != ""
}

func (a *App) remotePeers() bool {
	ip := net.ParseIP(a.bind)
	return a.multiProcess() && (ip == nil || !ip.IsLoopback())
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
	maxPeerBody     = 16 << 20
	peerTimeout     = 30 * time.Second
	peerIdleTimeout = 90 * time.Second
	probeTimeout    = 5 * time.Second
	bankFlushEvery  = 500 * time.Millisecond
)

func (a *App) coreDo(method, path string, in, out any) (int, error) {
	return a.peerDo(a.coreURL, method, path, in, out)
}

func (a *App) peerDo(base, method, path string, in, out any) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), peerTimeout)
	defer cancel()
	return a.peerRequest(ctx, base, method, path, nil, in, out)
}

type peerError struct {
	Status  int
	Code    string
	Message string
}

func (e *peerError) Error() string {
	return e.Message
}

func newPeerClient() *http.Client {
	return &http.Client{Timeout: peerTimeout, Transport: &http.Transport{IdleConnTimeout: peerIdleTimeout, MaxIdleConnsPerHost: 2}}
}

func (a *App) peerRequest(ctx context.Context, base, method, path string, header http.Header, in, out any) (int, error) {
	var rd io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(base, "/")+path, rd)
	if err != nil {
		return 0, err
	}
	for k, v := range header {
		req.Header[k] = v
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
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		msg := e.Error.Message
		if msg == "" {
			msg = resp.Status
		}
		return resp.StatusCode, &peerError{Status: resp.StatusCode, Code: e.Error.Code, Message: msg}
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
		peers:     newPeerClient(),
		probeHTTP: &http.Client{Timeout: probeTimeout},
		wake:      make(chan struct{}, 1),
		sending:   make(chan struct{}, 4),
		role:      RoleBank,
		bankID:    b.ID,
		coreURL:   o.CoreURL,
		bind:      o.Bind,
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
	ls, err := openLedgerSource(context.Background(), cfg, a.ctl, a.clock, b)
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
			a.mu.Lock()
			a.sweep()
			a.mu.Unlock()
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
