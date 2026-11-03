package connect

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/pki"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

func (a *App) prepareOpenBanking() error {
	a.obs = map[string]*openbanking.Server{}
	if err := a.cfg.EnsureCertificates(); err != nil {
		return err
	}
	used := map[int]bool{a.cfg.Connect.PartnerPort: true, a.cfg.Connect.WidgetPort: true, a.cfg.Connect.ControlPort: true}
	for _, b := range a.cfg.Banks {
		used[b.Port], used[b.APIPort] = true, true
	}
	changed := false
	for _, b := range a.cfg.Banks {
		if b.APIPort != 0 {
			continue
		}
		p, err := a.cfg.AllocatePort(b.Port+a.cfg.APIPortOffset(), used, portFree)
		if err != nil {
			return fmt.Errorf("%s: bank API: %w", b.ID, err)
		}
		b.APIPort, used[p], changed = p, true, true
	}
	if changed {
		if err := a.cfg.Save(); err != nil {
			return err
		}
	}
	return a.ensureRegistry()
}

func (a *App) ensureRegistry() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ensureRegistryLocked()
}

func (a *App) ensureRegistryLocked() error {
	key, err := a.cfg.SigningKey()
	if err != nil {
		return err
	}
	c := &RegistryClient{
		ID:           a.cfg.Connect.RegistryID,
		Name:         a.cfg.Connect.BrandName,
		LegalName:    a.cfg.Connect.LegalName,
		RegistryID:   a.cfg.Connect.RegistryID,
		CAC:          a.cfg.Connect.CACNumber,
		Role:         config.AggregatorRole,
		CertCN:       config.AggregatorCN,
		JWK:          openbanking.JWKOf(&key.PublicKey, "connect-1"),
		RedirectURIs: []string{a.cfg.WidgetURL() + "/bank/callback"},
		RegisteredAt: a.clock.Real(),
	}
	a.regMu.Lock()
	defer a.regMu.Unlock()
	if old := a.st.Registry[c.ID]; old != nil {
		c.RegisteredAt = old.RegisteredAt
		c.RedirectURIs = old.RedirectURIs
		if !slices.Contains(c.RedirectURIs, a.cfg.WidgetURL()+"/bank/callback") {
			c.RedirectURIs = append(c.RedirectURIs, a.cfg.WidgetURL()+"/bank/callback")
		}
	}
	a.st.Registry[c.ID] = c
	a.dirty()
	return nil
}

func localHost(h string) bool {
	return h == "localhost" || strings.HasSuffix(h, ".localhost") || net.ParseIP(h).IsLoopback()
}

var registryID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,39}$`)

type RegistryRequest struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	LegalName    string   `json:"legal_name"`
	CAC          string   `json:"cac"`
	CertCN       string   `json:"cert_cn"`
	Role         string   `json:"role"`
	PublicKey    string   `json:"public_key_pem"`
	RedirectURIs []string `json:"redirect_uris"`
}

var ErrRegistry = errors.New("registry")

func (a *App) AddRegistryClient(req RegistryRequest) (*RegistryClient, error) {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrRegistry, fmt.Sprintf(format, args...))
	}
	if !registryID.MatchString(req.ID) {
		return nil, bad("the ID must be letters, digits, dots, dashes and underscores, up to 40 characters")
	}
	if req.CertCN == "" {
		return nil, bad("cert_cn is required: the common name of the client's certificate")
	}
	if req.Role == "" {
		req.Role = config.AggregatorRole
	}
	if req.Role != config.AggregatorRole {
		return nil, bad("role must be %s", config.AggregatorRole)
	}
	if len(req.RedirectURIs) == 0 {
		return nil, bad("at least one redirect URI is required")
	}
	for _, u := range req.RedirectURIs {
		p, err := url.Parse(u)
		if err != nil || p.Host == "" || p.Fragment != "" || (p.Scheme != "https" && !(p.Scheme == "http" && localHost(p.Hostname()))) {
			return nil, bad("%q must be an https URL, or an http URL on localhost", u)
		}
	}
	block, _ := pem.Decode([]byte(req.PublicKey))
	if block == nil {
		return nil, bad("public_key_pem must hold a PEM public key")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, bad("the public key could not be read")
	}
	ec, ok := pub.(*ecdsa.PublicKey)
	if !ok || ec.Curve != elliptic.P256() {
		return nil, bad("the public key must be an EC P-256 key")
	}
	if req.Name == "" {
		req.Name = req.ID
	}
	if req.LegalName == "" {
		req.LegalName = req.Name
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.regMu.Lock()
	defer a.regMu.Unlock()
	if a.st.Registry[req.ID] != nil {
		return nil, bad("%s is already registered", req.ID)
	}
	for _, c := range a.st.Registry {
		if c.CertCN == req.CertCN {
			return nil, bad("certificate name %s already belongs to %s", req.CertCN, c.ID)
		}
	}
	c := &RegistryClient{ID: req.ID, Name: req.Name, LegalName: req.LegalName, RegistryID: req.ID, CAC: req.CAC, Role: req.Role, CertCN: req.CertCN, JWK: openbanking.JWKOf(ec, req.ID), RedirectURIs: req.RedirectURIs, RegisteredAt: a.clock.Real()}
	a.st.Registry[c.ID] = c
	a.dirty()
	return c, nil
}

func (a *App) RegistryClients() []*RegistryClient {
	a.regMu.Lock()
	defer a.regMu.Unlock()
	out := []*RegistryClient{}
	for _, c := range a.st.Registry {
		out = append(out, c)
	}
	slices.SortFunc(out, func(x, y *RegistryClient) int { return x.RegisteredAt.Compare(y.RegisteredAt) })
	return out
}

func (a *App) ConsentLedger(bank string, limit int) []ConsentRecord {
	a.regMu.Lock()
	defer a.regMu.Unlock()
	out := []ConsentRecord{}
	for i := len(a.st.ConsentLedger) - 1; i >= 0 && len(out) < limit; i-- {
		if bank == "" || a.st.ConsentLedger[i].Bank == bank {
			out = append(out, a.st.ConsentLedger[i])
		}
	}
	return out
}

type registryView struct {
	a *App
}

func (r registryView) convert(c *RegistryClient) (*openbanking.Client, bool) {
	pub, err := c.JWK.Public()
	if err != nil {
		return nil, false
	}
	return &openbanking.Client{ID: c.ID, Name: c.Name, LegalName: c.LegalName, RegistryID: c.RegistryID, CAC: c.CAC, Role: c.Role, CertCN: c.CertCN, Keys: []*ecdsa.PublicKey{pub}, RedirectURIs: slices.Clone(c.RedirectURIs)}, true
}

func (r registryView) Client(id string) (*openbanking.Client, bool) {
	r.a.regMu.Lock()
	defer r.a.regMu.Unlock()
	if c := r.a.st.Registry[id]; c != nil {
		return r.convert(c)
	}
	return nil, false
}

func (r registryView) ClientByCert(cn string) (*openbanking.Client, bool) {
	r.a.regMu.Lock()
	defer r.a.regMu.Unlock()
	for _, c := range r.a.st.Registry {
		if c.CertCN == cn {
			return r.convert(c)
		}
	}
	return nil, false
}

type consentLedger struct {
	a *App
}

func (l consentLedger) Record(e openbanking.ConsentEvent) {
	if l.a.role == RoleBank {
		go func() {
			if _, err := l.a.coreDo(http.MethodPost, "/internal/consents", e, nil); err != nil {
				l.a.log.Warn("consent ledger not updated", "consent", e.ConsentID, "err", err)
			}
		}()
		return
	}
	l.a.regMu.Lock()
	defer l.a.regMu.Unlock()
	l.a.st.ConsentLedger = append(l.a.st.ConsentLedger, ConsentRecord{Seq: int64(len(l.a.st.ConsentLedger) + 1), ConsentID: e.ConsentID, Bank: e.Bank, ClientID: e.ClientID, PartnerID: e.PartnerID, Status: string(e.Status), Via: e.Via, At: e.At})
	l.a.isDirty.Store(true)
}

type bankData struct {
	a    *App
	bank *config.Bank
}

func (d bankData) ledger() (*ledgerSource, error) {
	if l := d.a.obLedger(d.bank.ID); l != nil {
		return l, nil
	}
	return nil, errors.New("the bank ledger is not open")
}

func (d bankData) find(nuban string) (accountRef, error) {
	acc, err := d.a.world.FindAccount(nuban)
	if err != nil || acc.Institution != d.bank.ID || acc.NUBAN != nuban {
		return accountRef{}, openbanking.ErrNotFound
	}
	return accountRef{acc.Key, acc.NUBAN, acc.Name, acc.Type, acc.Currency}, nil
}

type accountRef struct {
	Key, NUBAN, Name, Type, Currency string
}

func (r accountRef) info() openbanking.AccountInfo {
	return openbanking.AccountInfo{ID: r.NUBAN, Name: r.Name, Type: r.Type, Currency: r.Currency}
}

func (d bankData) Accounts(customer string) ([]openbanking.AccountInfo, error) {
	out := []openbanking.AccountInfo{}
	for _, acc := range d.a.world.AccountsAt(customer, d.bank.ID) {
		out = append(out, accountRef{acc.Key, acc.NUBAN, acc.Name, acc.Type, acc.Currency}.info())
	}
	return out, nil
}

func (d bankData) Account(nuban string) (openbanking.AccountInfo, error) {
	r, err := d.find(nuban)
	return r.info(), err
}

func (d bankData) Balance(nuban string) (openbanking.Balance, error) {
	r, err := d.find(nuban)
	if err != nil {
		return openbanking.Balance{}, err
	}
	l, err := d.ledger()
	if err != nil {
		return openbanking.Balance{}, err
	}
	now := d.a.clock.Now()
	ledger, err := l.LedgerAt(r.Key, now)
	if err != nil {
		return openbanking.Balance{}, err
	}
	pending, err := l.PendingAt(r.Key, now)
	if err != nil {
		return openbanking.Balance{}, err
	}
	b := openbanking.Balance{Ledger: ledger, Available: ledger, Currency: r.Currency}
	if pending < 0 {
		b.Available += pending
	}
	return b, nil
}

func (d bankData) BalanceOn(nuban string, day clock.Date) (int64, error) {
	r, err := d.find(nuban)
	if err != nil {
		return 0, err
	}
	l, err := d.ledger()
	if err != nil {
		return 0, err
	}
	return l.EndOfDay(r.Key, day)
}

func (d bankData) Transactions(nuban string, from, to clock.Date) ([]openbanking.Txn, error) {
	r, err := d.find(nuban)
	if err != nil {
		return nil, err
	}
	l, err := d.ledger()
	if err != nil {
		return nil, err
	}
	rows, err := l.Fetch(r.Key, d.a.clock.Now(), from)
	if err != nil {
		return nil, err
	}
	out := []openbanking.Txn{}
	for _, p := range rows {
		if p.ValueDate.Before(from) || p.ValueDate.After(to) {
			continue
		}
		id := p.Key
		if !d.bank.Stable() {
			id = world.ID("t", p.Key, clock.DateOf(d.a.clock.Now()).String())
		}
		t := openbanking.Txn{ID: id, HadPending: p.HadPending, Pending: p.Pending, Amount: p.Amount, BookedAt: p.BookedAt, ValueDate: p.ValueDate, Narration: p.Narration, SessionID: p.SessionID, BankRef: p.BankRef, Channel: p.Channel, BalanceAfter: p.BalanceAfter, Seq: p.Seq}
		if p.Counterparty != nil {
			t.Counterparty = &openbanking.Counterparty{Name: p.Counterparty.Name, AccountNumber: p.Counterparty.AccountNumber, BankCode: p.Counterparty.BankCode, BankName: p.Counterparty.BankName}
		}
		out = append(out, t)
	}
	return out, nil
}

func (d bankData) Ping() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	l, err := d.ledger()
	if err != nil {
		return err
	}
	return l.Ping(ctx)
}

func (a *App) openBanking(id string) *openbanking.Server {
	a.obMu.Lock()
	defer a.obMu.Unlock()
	if s := a.obs[id]; s != nil {
		return s
	}
	b := a.cfg.Bank(id)
	l := a.obLedger(id)
	if b == nil || l == nil {
		return nil
	}
	first := l.st.Pool
	s := openbanking.New(openbanking.Config{
		BankID:       b.ID,
		BankName:     b.Name,
		Issuer:       b.APIURL(),
		AuthorizeURL: b.URL() + "/authorize",
		HistoryDays:  b.HistoryDays,
		Store: &openbanking.PGStore{Pool: func() *pgxpool.Pool {
			if cur := a.obLedger(id); cur != nil {
				return cur.st.Pool
			}
			return first
		}},
		Registry:      registryView{a},
		Data:          bankData{a, b},
		Ledger:        consentLedger{a},
		StableIDs:     b.Stable(),
		Now:           a.clock.Real,
		Biz:           a.clock.Now,
		ConsentMonths: b.API.ConsentMonths,
		RefreshDays:   b.API.RefreshDays,
		DormantDays:   b.API.DormantDays,
		AssertionLife: time.Duration(b.API.AssertionSeconds) * time.Second,
	})
	if err := s.Resume(context.Background()); err != nil {
		a.log.Error("open banking requests not cleared", "bank", id, "err", err)
	}
	a.obs[id] = s
	return s
}

func (a *App) forgetOpenBanking(id string) {
	a.obMu.Lock()
	delete(a.obs, id)
	a.obMu.Unlock()
}

func (a *App) bankAPITLS(b *config.Bank) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(a.cfg.Path("banks", b.ID, "server.crt"), a.cfg.Path("banks", b.ID, "server.key"))
	if err != nil {
		return nil, err
	}
	pool, err := pki.LoadPool(a.cfg.Path("ca", "root.crt"))
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool, MinVersion: tls.VersionTLS12}, nil
}

func (a *App) startBankAPI(b *config.Bank) (*http.Server, error) {
	if b.APIPort == 0 {
		return nil, fmt.Errorf("%s has no API port", b.ID)
	}
	if err := a.cfg.EnsureBankCertificates(nil, time.Time{}); err != nil {
		return nil, err
	}
	ob := a.openBanking(b.ID)
	if ob == nil {
		return nil, fmt.Errorf("no ledger for %s", b.ID)
	}
	tc, err := a.bankAPITLS(b)
	if err != nil {
		return nil, err
	}
	ls, err := listenLocal(b.APIPort)
	if err != nil {
		return nil, err
	}
	h := ob.Handler()
	guarded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.freshen()
		if (a.role == RoleBank && (!a.synced.Load() || a.paused.Load())) || a.bankPhase(b.ID) == PhaseSuspended || a.obLedger(b.ID) == nil || a.world.Down(b.ID, a.clock.Now()) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"temporarily_unavailable","error_description":"The bank is not available right now."}`))
			return
		}
		h.ServeHTTP(w, r)
	})
	srv := &http.Server{Handler: guarded, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 2 * time.Minute, ErrorLog: log.New(quietTLS{a}, "", 0)}
	for _, l := range ls {
		go func(l net.Listener) { _ = srv.Serve(tls.NewListener(l, tc)) }(l)
	}
	return srv, nil
}

func (a *App) ctlRegistryClients(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"data": a.RegistryClients()})
}

func (a *App) ctlRegistryAdd(w http.ResponseWriter, r *http.Request) {
	var req RegistryRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "Send a JSON body with id, cert_cn, public_key_pem and redirect_uris.")
		return
	}
	c, err := a.AddRegistryClient(req)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrRegistry) {
			status = http.StatusUnprocessableEntity
		}
		writeError(w, r, status, "invalid_request", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (a *App) ctlConsentLedger(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"data": a.ConsentLedger(r.URL.Query().Get("bank"), 200)})
}
