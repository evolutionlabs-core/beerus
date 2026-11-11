package connect

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/pki"
)

const (
	assertionType  = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
	refreshMargin  = time.Minute
	refreshAhead   = 2 * time.Minute
	bankCallBudget = 20 * time.Second

	bankDialTimeout      = 5 * time.Second
	bankIdleTimeout      = 90 * time.Second
	bankIdleConnsPerHost = 4
	bankBodyLimit        = 16 << 20
	bankRetryWait        = 100 * time.Millisecond
)

type bankToken struct {
	Access         string
	Refresh        string
	AccessExpires  time.Time
	RefreshExpires time.Time
	ConsentID      string
}

type bankError struct {
	Status       int
	Code         string
	ConsentError string
	Desc         string
}

func (e *bankError) Error() string {
	return fmt.Sprintf("bank answered %d %s: %s", e.Status, e.Code, e.Desc)
}

type bankDown struct {
	err error
}

func (e *bankDown) Error() string {
	return "the bank cannot be reached: " + e.err.Error()
}

var errReauth = errors.New("the customer must consent again")

func consentCode(err error) string {
	var be *bankError
	if errors.As(err, &be) {
		if be.ConsentError != "" {
			return be.ConsentError
		}
		return be.Code
	}
	return ""
}

func isDown(err error) bool {
	var bd *bankDown
	return errors.As(err, &bd)
}

func (a *App) bankClient() (*http.Client, error) {
	a.mu.Lock()
	cl := a.obHTTP
	a.mu.Unlock()
	if cl != nil {
		return cl, nil
	}
	built, err := a.newBankClient()
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.obHTTP == nil {
		a.obHTTP = built
	}
	return a.obHTTP, nil
}

func (a *App) newBankClient() (*http.Client, error) {
	pool, err := pki.LoadPool(a.cfg.Path("ca", "root.crt"))
	if err != nil {
		return nil, err
	}
	certPath, keyPath := a.cfg.Path("connect", "client.crt"), a.cfg.Path("connect", "client.key")
	tc := &tls.Config{
		RootCAs:    pool,
		MinVersion: tls.VersionTLS12,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			pair, err := tls.LoadX509KeyPair(certPath, keyPath)
			return &pair, err
		},
	}
	dialer := &net.Dialer{Timeout: bankDialTimeout}
	var rt http.RoundTripper = &http.Transport{
		TLSClientConfig:     tc,
		IdleConnTimeout:     bankIdleTimeout,
		MaxIdleConnsPerHost: bankIdleConnsPerHost,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err == nil && !multiProcess() && strings.HasSuffix(host, ".localhost") {
				addr = net.JoinHostPort("127.0.0.1", port)
			}
			return dialer.DialContext(ctx, network, addr)
		},
	}
	if a.bankWrap != nil {
		rt = a.bankWrap(rt)
	}
	return &http.Client{Timeout: bankCallBudget, Transport: rt}, nil
}

func (a *App) signingKey() (*ecdsa.PrivateKey, error) {
	a.mu.Lock()
	key := a.obKey
	a.mu.Unlock()
	if key != nil {
		return key, nil
	}
	loaded, err := a.cfg.SigningKey()
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.obKey == nil {
		a.obKey = loaded
	}
	return a.obKey, nil
}

func (a *App) assertion(b *config.Bank) (string, error) {
	key, err := a.signingKey()
	if err != nil {
		return "", err
	}
	id := a.cfg.Connect.RegistryID
	return openbanking.Sign(key, "connect-1", map[string]any{"iss": id, "sub": id, "aud": b.APIURL(), "jti": newSecret("j_", 12), "exp": a.clock.Real().Add(time.Minute).Unix()})
}

func (a *App) bankCall(b *config.Bank, method, path string, form url.Values, token string, out any) error {
	cl, err := a.bankClient()
	if err != nil {
		return err
	}
	var body io.Reader
	if form != nil {
		jwt, err := a.assertion(b)
		if err != nil {
			return err
		}
		form.Set("client_id", a.cfg.Connect.RegistryID)
		form.Set("client_assertion_type", assertionType)
		form.Set("client_assertion", jwt)
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, b.APIURL()+path, body)
	if err != nil {
		return err
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := cl.Do(req)
	if err != nil {
		return &bankDown{err}
	}
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, bankBodyLimit+1))
	resp.Body.Close()
	if readErr != nil {
		return fmt.Errorf("read bank response: %w", readErr)
	}
	if len(raw) > bankBodyLimit {
		return fmt.Errorf("bank response is larger than the %d byte limit", bankBodyLimit)
	}
	if resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusBadGateway {
		return &bankDown{fmt.Errorf("HTTP %d", resp.StatusCode)}
	}
	if resp.StatusCode >= 300 {
		be := &bankError{Status: resp.StatusCode}
		var m struct {
			Error        string `json:"error"`
			Desc         string `json:"error_description"`
			ConsentError string `json:"consent_error"`
		}
		_ = json.Unmarshal(raw, &m)
		be.Code, be.Desc, be.ConsentError = m.Error, m.Desc, m.ConsentError
		return be
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func pkcePair() (string, string) {
	verifier := newSecret("", 32)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

func (a *App) callbackURL() string {
	return a.cfg.WidgetURL() + "/bank/callback"
}

type tokenReply struct {
	Access           string `json:"access_token"`
	Refresh          string `json:"refresh_token"`
	ExpiresIn        int    `json:"expires_in"`
	RefreshExpiresIn int    `json:"refresh_expires_in"`
	ConsentID        string `json:"consent_id"`
}

func (a *App) toToken(r tokenReply) bankToken {
	now := a.clock.Real()
	return bankToken{Access: r.Access, Refresh: r.Refresh, AccessExpires: now.Add(time.Duration(r.ExpiresIn) * time.Second), RefreshExpires: now.Add(time.Duration(r.RefreshExpiresIn) * time.Second), ConsentID: r.ConsentID}
}

func (a *App) bankFromLocked(ls *LinkSession) clock.Date {
	from := ls.StartDate
	for _, c := range a.st.Connections {
		if c.PartnerID != ls.PartnerID || c.WorkspaceID != ls.WorkspaceID || c.Institution != ls.Institution {
			continue
		}
		for _, id := range c.AccountIDs {
			rec := a.st.Accounts[id]
			if rec == nil || !rec.HistoryDone || rec.AsAt.IsZero() {
				continue
			}
			if d := clock.DateOf(rec.AsAt); d.Before(from) {
				from = d
			}
		}
	}
	return from
}

func (a *App) parFormLocked(ls *LinkSession, state, challenge string) url.Values {
	p := a.partnerOf(ls.PartnerID)
	form := url.Values{
		"response_type":         {"code"},
		"redirect_uri":          {a.callbackURL()},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"scope":                 {strings.Join(openbanking.Scopes, " ")},
		"partner_id":            {ls.PartnerID},
		"transaction_from":      {a.bankFromLocked(ls).String()},
	}
	if p != nil {
		form.Set("partner_name", p.Name)
		form.Set("partner_legal_name", p.LegalName)
		form.Set("purpose", p.Purpose)
	}
	return form
}

func (a *App) bankPAR(b *config.Bank, form url.Values) (string, error) {
	var out struct {
		RequestURI string `json:"request_uri"`
	}
	if err := a.bankCall(b, http.MethodPost, "/par", form, "", &out); err != nil {
		return "", err
	}
	return out.RequestURI, nil
}

func (a *App) bankExchange(b *config.Bank, code, verifier string) (bankToken, error) {
	var r tokenReply
	err := a.bankCall(b, http.MethodPost, "/token", url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {a.callbackURL()}}, "", &r)
	if err != nil {
		return bankToken{}, err
	}
	return a.toToken(r), nil
}

type refreshPlan struct {
	vid     string
	entry   *VaultEntry
	refresh string
}

func (a *App) refreshPlanLocked(vid string) (refreshPlan, error) {
	e := a.st.Vault[vid]
	if e == nil {
		return refreshPlan{}, errReauth
	}
	refresh, err := a.vault.open(e.Refresh, vid+"/refresh")
	if err != nil {
		return refreshPlan{}, errReauth
	}
	return refreshPlan{vid: vid, entry: e, refresh: refresh}, nil
}

func (a *App) refreshCall(b *config.Bank, refresh string) (tokenReply, error) {
	var r tokenReply
	for attempt := 0; ; attempt++ {
		err := a.bankCall(b, http.MethodPost, "/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}}, "", &r)
		if err == nil || !isDown(err) || attempt == 1 {
			return r, err
		}
		time.Sleep(bankRetryWait)
	}
}

func (a *App) refreshApplyLocked(p refreshPlan, r tokenReply, err error) error {
	cur := a.st.Vault[p.vid]
	if cur == nil {
		return errReauth
	}
	if err != nil && cur != p.entry {
		return nil
	}
	if err != nil {
		var be *bankError
		if errors.As(err, &be) && be.Code == "invalid_grant" {
			if cc := consentCode(err); cc != "" && cc != "invalid_grant" {
				return err
			}
			return errReauth
		}
		return err
	}
	return a.vaultPut(p.vid, a.toToken(r))
}

func (a *App) refreshToken(vid string, b *config.Bank) error {
	a.mu.Lock()
	p, err := a.refreshPlanLocked(vid)
	a.mu.Unlock()
	if err != nil {
		return err
	}
	r, err := a.refreshCall(b, p.refresh)
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.refreshApplyLocked(p, r, err)
}

func (a *App) accessTokenLocked(vid string, force bool) (string, bool, error) {
	e := a.st.Vault[vid]
	if e == nil {
		return "", false, errReauth
	}
	if force || !e.AccessExpires.After(a.clock.Real().Add(refreshMargin)) {
		return "", true, nil
	}
	tok, err := a.openAccessLocked(vid)
	return tok, false, err
}

func (a *App) openAccessLocked(vid string) (string, error) {
	e := a.st.Vault[vid]
	if e == nil {
		return "", errReauth
	}
	tok, err := a.vault.open(e.Access, vid+"/access")
	if err != nil {
		return "", errReauth
	}
	return tok, nil
}

func (a *App) accessToken(vid string, b *config.Bank, force bool) (string, error) {
	a.mu.Lock()
	tok, needsRefresh, err := a.accessTokenLocked(vid, force)
	a.mu.Unlock()
	if err != nil || !needsRefresh {
		return tok, err
	}
	if err := a.refreshToken(vid, b); err != nil {
		return "", err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.openAccessLocked(vid)
}

func (a *App) bankGet(b *config.Bank, vid, path string, out any) error {
	for attempt := 0; ; attempt++ {
		tok, err := a.accessToken(vid, b, attempt > 0)
		if err != nil {
			return err
		}
		err = a.bankCall(b, http.MethodGet, path, nil, tok, out)
		var be *bankError
		if attempt == 0 && errors.As(err, &be) && be.Status == http.StatusUnauthorized && be.Code == "invalid_token" {
			continue
		}
		return err
	}
}

func (a *App) bankRevoke(b *config.Bank, consentID string) error {
	err := a.bankCall(b, http.MethodDelete, "/consents/"+url.PathEscape(consentID), nil, "", nil)
	var be *bankError
	if errors.As(err, &be) && be.Status == http.StatusNotFound {
		return nil
	}
	return err
}

func (a *App) revokeConnectionAtBank(connID string) error {
	a.mu.Lock()
	var b *config.Bank
	consent := ""
	if c := a.st.Connections[connID]; c != nil {
		b, consent = a.cfg.Bank(c.Institution), c.ConsentID
	}
	a.mu.Unlock()
	if b != nil && consent != "" {
		if err := a.bankRevoke(b, consent); err != nil {
			return err
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if c := a.st.Connections[connID]; c != nil && c.ConsentID != consent {
		return nil
	}
	a.vaultDrop(connID)
	return nil
}

func (a *App) revokeOrQueue(b *config.Bank, consentID string) {
	if consentID == "" {
		return
	}
	err := a.bankRevoke(b, consentID)
	if err == nil {
		return
	}
	a.log.Warn("consent not revoked at the bank; will retry", "bank", b.ID, "consent", consentID, "err", err)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.st.Revokes = append(a.st.Revokes, PendingRevoke{Bank: b.ID, ConsentID: consentID, Attempts: 1, Next: a.clock.Real().Add(eventDelay(1))})
	a.dirty()
}

type dueRevoke struct {
	PendingRevoke
	bank *config.Bank
}

func (a *App) dueRevokesLocked() []dueRevoke {
	now := a.clock.Real()
	var due []dueRevoke
	kept := make([]PendingRevoke, 0, len(a.st.Revokes))
	for _, p := range a.st.Revokes {
		b := a.cfg.Bank(p.Bank)
		if b == nil {
			a.log.Warn("pending revoke dropped because the bank is gone", "bank", p.Bank, "consent", p.ConsentID)
			continue
		}
		kept = append(kept, p)
		if !now.Before(p.Next) {
			due = append(due, dueRevoke{PendingRevoke: p, bank: b})
		}
	}
	if len(kept) != len(a.st.Revokes) {
		a.st.Revokes = kept
		a.dirty()
	}
	return due
}

func (a *App) settleRevokeLocked(p PendingRevoke, err error) {
	i := slices.IndexFunc(a.st.Revokes, func(q PendingRevoke) bool { return q.Bank == p.Bank && q.ConsentID == p.ConsentID })
	if i < 0 {
		return
	}
	a.dirty()
	if err == nil {
		a.st.Revokes = slices.Delete(a.st.Revokes, i, i+1)
		return
	}
	a.st.Revokes[i].Attempts = p.Attempts + 1
	a.st.Revokes[i].Next = a.clock.Real().Add(eventDelay(p.Attempts + 1))
	a.log.Warn("consent still not revoked at the bank", "bank", p.Bank, "consent", p.ConsentID, "attempt", p.Attempts+1, "err", err)
}

func (a *App) retryRevokes() {
	a.mu.Lock()
	due := a.dueRevokesLocked()
	a.mu.Unlock()
	for _, d := range due {
		err := a.bankRevoke(d.bank, d.ConsentID)
		a.mu.Lock()
		a.settleRevokeLocked(d.PendingRevoke, err)
		a.mu.Unlock()
	}
}

func (a *App) refreshTokens() {
	a.mu.Lock()
	ids := make([]string, 0, len(a.st.Connections))
	for id := range a.st.Connections {
		ids = append(ids, id)
	}
	a.mu.Unlock()
	sort.Strings(ids)
	for _, id := range ids {
		a.refreshConnectionToken(id)
	}
}

func (a *App) refreshConnectionToken(id string) {
	a.mu.Lock()
	c := a.st.Connections[id]
	if c == nil {
		a.mu.Unlock()
		return
	}
	switch c.Status {
	case "revoked", "expired", "reauth_required":
		a.mu.Unlock()
		return
	}
	e := a.st.Vault[id]
	b := a.cfg.Bank(c.Institution)
	if e == nil || b == nil || e.AccessExpires.After(a.clock.Real().Add(refreshAhead)) {
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()
	err := a.refreshToken(id, b)
	if err == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if c := a.st.Connections[id]; c != nil {
		a.bankFailedLocked(c, err)
	}
}

func (a *App) bankFailedLocked(c *Connection, err error) bool {
	switch {
	case consentCode(err) == "consent_revoked":
		a.setStatus(c, "revoked", "connection_revoked")
	case slices.Contains([]string{"consent_expired", "consent_reconsent_required", "consent_dormant"}, consentCode(err)):
		a.setStatus(c, "expired", "reauth_required")
	case consentCode(err) == "consent_not_authorised", errors.Is(err, errReauth):
		a.setStatus(c, "reauth_required", "reauth_required")
	case isDown(err):
		if c.Status != "institution_down" {
			c.StatusBeforeDown = c.Status
			a.setStatus(c, "institution_down", "institution_down")
		}
	default:
		return false
	}
	return true
}
