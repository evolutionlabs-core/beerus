package connect

import (
	"context"
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

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/pki"
)

const (
	assertionType  = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
	refreshMargin  = time.Minute
	refreshAhead   = 2 * time.Minute
	bankCallBudget = 20 * time.Second
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
	if a.obHTTP != nil {
		return a.obHTTP, nil
	}
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
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	var rt http.RoundTripper = &http.Transport{
		TLSClientConfig: tc,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err == nil && strings.HasSuffix(host, ".localhost") {
				addr = net.JoinHostPort("127.0.0.1", port)
			}
			return dialer.DialContext(ctx, network, addr)
		},
	}
	if a.bankWrap != nil {
		rt = a.bankWrap(rt)
	}
	a.obHTTP = &http.Client{Timeout: bankCallBudget, Transport: rt}
	return a.obHTTP, nil
}

func (a *App) assertion(b *config.Bank) (string, error) {
	if a.obKey == nil {
		key, err := a.cfg.SigningKey()
		if err != nil {
			return "", err
		}
		a.obKey = key
	}
	id := a.cfg.Connect.RegistryID
	return openbanking.Sign(a.obKey, "connect-1", map[string]any{"iss": id, "sub": id, "aud": b.APIURL(), "jti": newSecret("j_", 12), "exp": a.clock.Real().Add(time.Minute).Unix()})
}

func (a *App) bankDo(b *config.Bank, method, path string, form url.Values, token string, out any) error {
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
	a.mu.Unlock()
	resp, err := cl.Do(req)
	var raw []byte
	if err == nil {
		raw, _ = io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
	}
	a.mu.Lock()
	if err != nil {
		return &bankDown{err}
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

func (a *App) bankPAR(b *config.Bank, ls *LinkSession, state, challenge string) (string, error) {
	p := a.partnerOf(ls.PartnerID)
	form := url.Values{
		"response_type":         {"code"},
		"redirect_uri":          {a.callbackURL()},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"scope":                 {strings.Join(openbanking.Scopes, " ")},
		"partner_id":            {ls.PartnerID},
		"transaction_from":      {ls.StartDate.String()},
	}
	if p != nil {
		form.Set("partner_name", p.Name)
		form.Set("partner_legal_name", p.LegalName)
		form.Set("purpose", p.Purpose)
	}
	var out struct {
		RequestURI string `json:"request_uri"`
	}
	if err := a.bankDo(b, http.MethodPost, "/par", form, "", &out); err != nil {
		return "", err
	}
	return out.RequestURI, nil
}

func (a *App) bankExchange(b *config.Bank, code, verifier string) (bankToken, error) {
	var r tokenReply
	err := a.bankDo(b, http.MethodPost, "/token", url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {a.callbackURL()}}, "", &r)
	if err != nil {
		return bankToken{}, err
	}
	return a.toToken(r), nil
}

func (a *App) refreshToken(vid string, b *config.Bank) error {
	e := a.st.Vault[vid]
	if e == nil {
		return errReauth
	}
	refresh, err := a.vault.open(e.Refresh, vid+"/refresh")
	if err != nil {
		return errReauth
	}
	var r tokenReply
	for attempt := 0; ; attempt++ {
		err = a.bankDo(b, http.MethodPost, "/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}}, "", &r)
		if err == nil || !isDown(err) || attempt == 1 {
			break
		}
		a.mu.Unlock()
		time.Sleep(100 * time.Millisecond)
		a.mu.Lock()
	}
	cur := a.st.Vault[vid]
	if cur == nil {
		return errReauth
	}
	if err != nil && cur != e {
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
	return a.vaultPut(vid, a.toToken(r))
}

func (a *App) accessToken(vid string, b *config.Bank, force bool) (string, error) {
	e := a.st.Vault[vid]
	if e == nil {
		return "", errReauth
	}
	if force || !e.AccessExpires.After(a.clock.Real().Add(refreshMargin)) {
		if err := a.refreshToken(vid, b); err != nil {
			return "", err
		}
		e = a.st.Vault[vid]
	}
	tok, err := a.vault.open(e.Access, vid+"/access")
	if err != nil {
		return "", errReauth
	}
	return tok, nil
}

func (a *App) bankGet(b *config.Bank, vid, path string, out any) error {
	for attempt := 0; ; attempt++ {
		tok, err := a.accessToken(vid, b, attempt > 0)
		if err != nil {
			return err
		}
		err = a.bankDo(b, http.MethodGet, path, nil, tok, out)
		var be *bankError
		if attempt == 0 && errors.As(err, &be) && be.Status == http.StatusUnauthorized && be.Code == "invalid_token" {
			continue
		}
		return err
	}
}

func (a *App) bankRevoke(b *config.Bank, consentID string) error {
	err := a.bankDo(b, http.MethodDelete, "/consents/"+url.PathEscape(consentID), nil, "", nil)
	var be *bankError
	if errors.As(err, &be) && be.Status == http.StatusNotFound {
		return nil
	}
	return err
}

func (a *App) revokeOrQueue(b *config.Bank, consentID string) {
	if consentID == "" {
		return
	}
	if err := a.bankRevoke(b, consentID); err != nil {
		a.log.Warn("consent not revoked at the bank; will retry", "bank", b.ID, "consent", consentID, "err", err)
		a.st.Revokes = append(a.st.Revokes, PendingRevoke{Bank: b.ID, ConsentID: consentID, Attempts: 1, Next: a.clock.Real().Add(eventDelay(1))})
		a.dirty()
	}
}

func (a *App) retryRevokes() {
	if !a.live() || len(a.st.Revokes) == 0 {
		return
	}
	pending := a.st.Revokes
	a.st.Revokes = nil
	for _, p := range pending {
		b := a.cfg.Bank(p.Bank)
		if b == nil {
			a.log.Warn("pending revoke dropped because the bank is gone", "bank", p.Bank, "consent", p.ConsentID)
			continue
		}
		if a.clock.Real().Before(p.Next) {
			a.st.Revokes = append(a.st.Revokes, p)
			continue
		}
		if err := a.bankRevoke(b, p.ConsentID); err != nil {
			p.Attempts++
			p.Next = a.clock.Real().Add(eventDelay(p.Attempts))
			a.log.Warn("consent still not revoked at the bank", "bank", p.Bank, "consent", p.ConsentID, "attempt", p.Attempts, "err", err)
			a.st.Revokes = append(a.st.Revokes, p)
		}
	}
	a.dirty()
}

type bankMeta struct {
	StableIDs bool `json:"stable_ids"`
}

func (a *App) bankMetaFor(b *config.Bank) (bankMeta, error) {
	if m, ok := a.metas[b.ID]; ok {
		return m, nil
	}
	var m bankMeta
	if err := a.bankDo(b, http.MethodGet, "/discover", nil, "", &m); err != nil {
		return m, err
	}
	if a.metas == nil {
		a.metas = map[string]bankMeta{}
	}
	a.metas[b.ID] = m
	return m, nil
}

func (a *App) refreshTokens() {
	if !a.live() {
		return
	}
	real := a.clock.Real()
	ids := make([]string, 0, len(a.st.Connections))
	for id := range a.st.Connections {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		c := a.st.Connections[id]
		if c == nil {
			continue
		}
		switch c.Status {
		case "revoked", "expired", "reauth_required":
			continue
		}
		e := a.st.Vault[c.ID]
		b := a.cfg.Bank(c.Institution)
		if e == nil || b == nil || e.AccessExpires.After(real.Add(refreshAhead)) {
			continue
		}
		if err := a.refreshToken(c.ID, b); err != nil {
			a.bankFailed(c, err)
		}
	}
}

func (a *App) bankFailed(c *Connection, err error) bool {
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

func (a *App) live() bool {
	return !a.worldOnly
}
