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
	"maps"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/bank/store"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/web"
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
	c := &openbanking.RegistryClient{
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

func (a *App) AddRegistryClient(req RegistryRequest) (*openbanking.RegistryClient, error) {
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
	c := &openbanking.RegistryClient{ID: req.ID, Name: req.Name, LegalName: req.LegalName, RegistryID: req.ID, CAC: req.CAC, Role: req.Role, CertCN: req.CertCN, JWK: openbanking.JWKOf(ec, req.ID), RedirectURIs: req.RedirectURIs, RegisteredAt: a.clock.Real()}
	a.st.Registry[c.ID] = c
	a.dirty()
	return c, nil
}

func (a *App) RegistryClients() []*openbanking.RegistryClient {
	a.regMu.Lock()
	defer a.regMu.Unlock()
	out := []*openbanking.RegistryClient{}
	for _, c := range a.st.Registry {
		out = append(out, c)
	}
	slices.SortFunc(out, func(x, y *openbanking.RegistryClient) int { return x.RegisteredAt.Compare(y.RegisteredAt) })
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

type memRegistry struct {
	a *App
}

func (r memRegistry) RegistryClient(id string) (*openbanking.RegistryClient, bool) {
	r.a.regMu.Lock()
	defer r.a.regMu.Unlock()
	c := r.a.st.Registry[id]
	return c, c != nil
}

func (r memRegistry) RegistryClientByCert(cn string) (*openbanking.RegistryClient, bool) {
	r.a.regMu.Lock()
	defer r.a.regMu.Unlock()
	for _, c := range r.a.st.Registry {
		if c.CertCN == cn {
			return c, true
		}
	}
	return nil, false
}

func (a *App) obBooks(id string) func() *store.Books {
	return func() *store.Books {
		if l := a.obLedger(id); l != nil {
			return l.Books
		}
		return nil
	}
}

func (a *App) openBanking(id string) *openbanking.Server {
	a.obMu.Lock()
	s := a.obs[id]
	a.obMu.Unlock()
	if s != nil {
		return s
	}
	b := a.cfg.Bank(id)
	if b == nil || a.obLedger(id) == nil {
		return nil
	}
	s, err := server.NewOpenBanking(server.OpenConfig{Bank: *b, Books: a.obBooks(id), Registry: memRegistry{a}, Ledger: consentLedger{a}, Clock: a.clock})
	if err != nil {
		a.log.Error("open banking not built", "bank", id, "err", err)
		return nil
	}
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

func (a *App) obServers() map[string]*openbanking.Server {
	a.obMu.Lock()
	defer a.obMu.Unlock()
	return maps.Clone(a.obs)
}

func (a *App) startBankAPI(b *config.Bank) (*http.Server, error) {
	if b.APIPort == 0 {
		return nil, fmt.Errorf("%s has no API port", b.ID)
	}
	ob := a.openBanking(b.ID)
	if ob == nil {
		return nil, fmt.Errorf("no ledger for %s", b.ID)
	}
	tc, err := server.OpenAPITLS(a.cfg.Dir, b.ID, a.cfg.Path("ca", "root.crt"))
	if err != nil {
		return nil, err
	}
	ls, err := a.link.Listen(b.APIPort)
	if err != nil {
		return nil, err
	}
	srv := &http.Server{Handler: server.OpenAPI(bankHost{a, b.ID}, ob, a.log), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 2 * time.Minute, ErrorLog: server.ErrorLog(a.log)}
	for _, l := range ls {
		go func(l net.Listener) { _ = srv.Serve(tls.NewListener(l, tc)) }(l)
	}
	return srv, nil
}

func (a *App) ctlRegistryClients(w http.ResponseWriter, r *http.Request) {
	web.WriteJSON(w, http.StatusOK, map[string]any{"data": a.RegistryClients()})
}

func (a *App) ctlRegistryAdd(w http.ResponseWriter, r *http.Request) {
	var req RegistryRequest
	if err := web.DecodeBody(r, &req); err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "Send a JSON body with id, cert_cn, public_key_pem and redirect_uris.")
		return
	}
	c, err := a.AddRegistryClient(req)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrRegistry) {
			status = http.StatusUnprocessableEntity
		}
		web.WriteError(w, r, status, "invalid_request", err.Error())
		return
	}
	web.WriteJSON(w, http.StatusCreated, c)
}

func (a *App) ctlConsentLedger(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, before := consentPageDefault, int64(0)
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > consentPageMax {
			web.WriteError(w, r, http.StatusBadRequest, "invalid_request", fmt.Sprintf("limit must be a number from 1 to %d.", consentPageMax))
			return
		}
		limit = n
	}
	if v := q.Get("cursor"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			web.WriteError(w, r, http.StatusBadRequest, "invalid_cursor", "That cursor is not valid. Use the next value from the last page.")
			return
		}
		before = n
	}
	data, next := a.ConsentLedger(q.Get("bank"), before, limit)
	out := map[string]any{"data": data}
	if next > 0 {
		out["next"] = strconv.FormatInt(next, 10)
	}
	web.WriteJSON(w, http.StatusOK, out)
}
