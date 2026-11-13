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
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/pki"
)

func (a *App) prepareOpenBanking() error {
	a.obs = map[string]*openbanking.Server{}
	if err := a.cfg.EnsureCertificates(a.clock.Real()); err != nil {
		return err
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

const (
	consentPageDefault = 200
	consentPageMax     = 1000
)

func (a *App) ConsentLedger(bank string, before int64, limit int) ([]ConsentRecord, int64) {
	a.regMu.Lock()
	defer a.regMu.Unlock()
	out := []ConsentRecord{}
	for i := len(a.st.ConsentLedger) - 1; i >= 0; i-- {
		e := a.st.ConsentLedger[i]
		if (bank != "" && e.Bank != bank) || (before > 0 && e.Seq >= before) {
			continue
		}
		if len(out) == limit {
			return out, out[len(out)-1].Seq
		}
		out = append(out, e)
	}
	return out, 0
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
	l.a.regMu.Lock()
	defer l.a.regMu.Unlock()
	if e.Seq != 0 {
		for i := len(l.a.st.ConsentLedger) - 1; i >= 0; i-- {
			if r := l.a.st.ConsentLedger[i]; r.Bank == e.Bank && r.SourceSeq != 0 {
				if r.SourceSeq >= e.Seq {
					return
				}
				break
			}
		}
	}
	l.a.st.ConsentLedger = append(l.a.st.ConsentLedger, ConsentRecord{Seq: int64(len(l.a.st.ConsentLedger) + 1), SourceSeq: e.Seq, ConsentID: e.ConsentID, Bank: e.Bank, ClientID: e.ClientID, PartnerID: e.PartnerID, Status: string(e.Status), Via: e.Via, At: e.At})
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

func (d bankData) find(nuban string) (ledger.Account, *ledgerSource, error) {
	l, err := d.ledger()
	if err != nil {
		return ledger.Account{}, nil, err
	}
	acc, err := l.Account(nuban)
	if errors.Is(err, ledger.ErrUnknownAccount) {
		return ledger.Account{}, nil, openbanking.ErrNotFound
	}
	if err != nil {
		return ledger.Account{}, nil, err
	}
	return acc, l, nil
}

func accountInfo(acc ledger.Account) openbanking.AccountInfo {
	return openbanking.AccountInfo{ID: acc.NUBAN, Name: acc.Name, Type: acc.Product, Currency: acc.Currency}
}

func (d bankData) Accounts(customer string) ([]openbanking.AccountInfo, error) {
	l, err := d.ledger()
	if err != nil {
		return nil, err
	}
	accs, err := l.Bank().CustomerAccounts(l.ctx, customer)
	if errors.Is(err, bank.ErrUnknownCustomer) {
		return []openbanking.AccountInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]openbanking.AccountInfo, 0, len(accs))
	for _, acc := range accs {
		out = append(out, accountInfo(acc))
	}
	return out, nil
}

func (d bankData) Account(nuban string) (openbanking.AccountInfo, error) {
	acc, _, err := d.find(nuban)
	return accountInfo(acc), err
}

func (d bankData) Balance(nuban string) (openbanking.Balance, error) {
	acc, l, err := d.find(nuban)
	if err != nil {
		return openbanking.Balance{}, err
	}
	bal, err := l.LedgerAt(nuban, d.a.clock.Now())
	if err != nil {
		return openbanking.Balance{}, err
	}
	return openbanking.Balance{Ledger: bal, Available: bal, Currency: acc.Currency}, nil
}

func (d bankData) BalanceOn(nuban string, day clock.Date) (int64, error) {
	_, l, err := d.find(nuban)
	if err != nil {
		return 0, err
	}
	return l.EndOfDay(nuban, day)
}

func (d bankData) Transactions(nuban string, from, to clock.Date) ([]openbanking.Txn, error) {
	_, l, err := d.find(nuban)
	if err != nil {
		return nil, err
	}
	rows, err := l.Fetch(nuban, d.a.clock.Now(), from)
	if err != nil {
		return nil, err
	}
	if err := d.a.markRemoved(context.Background(), rows); err != nil {
		return nil, err
	}
	out := []openbanking.Txn{}
	for _, p := range rows {
		if p.ValueDate.Before(from) || p.ValueDate.After(to) {
			continue
		}
		t := openbanking.Txn{ID: p.Key, Amount: p.Amount, BookedAt: p.BookedAt, ValueDate: p.ValueDate, Narration: p.Narration, SessionID: p.SessionID, BankRef: p.BankRef, Channel: p.Channel, BalanceAfter: p.BalanceAfter, Seq: p.Seq}
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
	s := a.obs[id]
	a.obMu.Unlock()
	if s != nil {
		return s
	}
	b := a.cfg.Bank(id)
	l := a.obLedger(id)
	if b == nil || l == nil {
		return nil
	}
	first := l.st.Pool
	s = openbanking.New(openbanking.Config{
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
		LedgerOutbox:  a.role == RoleBank,
		Now:           a.clock.Real,
		Biz:           a.clock.Now,
		ConsentMonths: b.API.ConsentMonths,
		RefreshDays:   b.API.RefreshDays,
		DormantDays:   b.API.DormantDays,
		AssertionLife: time.Duration(b.API.AssertionSeconds) * time.Second,
	})
	a.obMu.Lock()
	if cur := a.obs[id]; cur != nil {
		a.obMu.Unlock()
		return cur
	}
	a.obs[id] = s
	a.obMu.Unlock()
	if err := s.Resume(context.Background()); err != nil {
		a.log.Error("open banking requests not cleared", "bank", id, "err", err)
	}
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
	if err := a.cfg.EnsureBankCertificates(nil, a.clock.Real()); err != nil {
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
		if (a.role == RoleBank && (!a.synced.Load() || a.paused.Load())) || a.bankPhase(b.ID) == PhaseSuspended || a.obLedger(b.ID) == nil || a.bankDown(b.ID, a.clock.Now()) {
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
	q := r.URL.Query()
	limit, before := consentPageDefault, int64(0)
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > consentPageMax {
			writeError(w, r, http.StatusBadRequest, "invalid_request", fmt.Sprintf("limit must be a number from 1 to %d.", consentPageMax))
			return
		}
		limit = n
	}
	if v := q.Get("cursor"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			writeError(w, r, http.StatusBadRequest, "invalid_cursor", "That cursor is not valid. Use the next value from the last page.")
			return
		}
		before = n
	}
	data, next := a.ConsentLedger(q.Get("bank"), before, limit)
	out := map[string]any{"data": data}
	if next > 0 {
		out["next"] = strconv.FormatInt(next, 10)
	}
	writeJSON(w, http.StatusOK, out)
}
