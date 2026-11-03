package connect

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/pki"
)

const eventType = "application/secevent+jwt"

type inflight struct {
	mu sync.Mutex
	n  int
}

func (f *inflight) add(d int) {
	f.mu.Lock()
	f.n += d
	f.mu.Unlock()
}

func (f *inflight) wait(limit time.Duration) {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		n := f.n
		f.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (a *App) notifyConsent(b *config.Bank) func(openbanking.ConsentEvent) {
	return func(e openbanking.ConsentEvent) {
		a.events.add(1)
		go func() {
			defer a.events.add(-1)
			for attempt := 0; attempt < 3; attempt++ {
				if err := a.sendConsentEvent(b, e); err == nil {
					return
				} else {
					a.log.Warn("consent event not delivered", "bank", b.ID, "consent", e.ConsentID, "err", err)
				}
				time.Sleep(200 * time.Millisecond)
			}
		}()
	}
}

func (a *App) WaitConsentEvents() {
	a.events.wait(5 * time.Second)
}

func (a *App) bankSigner(b *config.Bank) (*ecdsa.PrivateKey, error) {
	raw, err := os.ReadFile(a.cfg.Path("banks", b.ID, "server.key"))
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("the bank key is malformed")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("the bank key is not an EC key")
	}
	return key, nil
}

func (a *App) sendConsentEvent(b *config.Bank, e openbanking.ConsentEvent) error {
	key, err := a.bankSigner(b)
	if err != nil {
		return err
	}
	now := a.clock.Real()
	jwt, err := openbanking.Sign(key, b.ID, map[string]any{
		"iss":        b.APIURL(),
		"aud":        a.cfg.WidgetURL(),
		"jti":        newSecret("ev_", 12),
		"iat":        now.Unix(),
		"exp":        now.Add(5 * time.Minute).Unix(),
		"consent_id": e.ConsentID,
		"status":     string(e.Status),
		"via":        e.Via,
		"at":         e.At.Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	pool, err := pki.LoadPool(a.cfg.Path("ca", "root.crt"))
	if err != nil {
		return err
	}
	cl := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsRoots(pool)}}
	resp, err := cl.Post(a.cfg.WidgetURL()+"/consent-events", eventType, bytes.NewReader([]byte(jwt)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return errors.New(resp.Status)
	}
	return nil
}

func (a *App) bankVerifyKey(id string) (*ecdsa.PublicKey, error) {
	b := a.cfg.Bank(id)
	if b == nil {
		return nil, errors.New("unknown bank")
	}
	raw, err := os.ReadFile(a.cfg.Path("banks", id, "server.crt"))
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("the bank certificate is malformed")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	pool, err := pki.LoadPool(a.cfg.Path("ca", "root.crt"))
	if err != nil {
		return nil, err
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, DNSName: id + ".localhost", CurrentTime: a.clock.Real()}); err != nil {
		return nil, err
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("the bank key is not an EC key")
	}
	return pub, nil
}

func (a *App) consentEvents(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 16<<10))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The event could not be read.")
		return
	}
	token := string(bytes.TrimSpace(raw))
	head, _, err := openbanking.Peek(token)
	kid, _ := head["kid"].(string)
	if err != nil || kid == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The event is not a signed token.")
		return
	}
	pub, err := a.bankVerifyKey(kid)
	if err != nil {
		writeError(w, r, http.StatusUnauthorized, "invalid_signature", "The bank's key could not be checked.")
		return
	}
	claims, err := openbanking.Verify(token, []*ecdsa.PublicKey{pub})
	if err != nil {
		writeError(w, r, http.StatusUnauthorized, "invalid_signature", "The signature does not match the bank.")
		return
	}
	b := a.cfg.Bank(kid)
	exp, _ := claims["exp"].(float64)
	jti, _ := claims["jti"].(string)
	consentID, _ := claims["consent_id"].(string)
	status, _ := claims["status"].(string)
	now := a.clock.Real()
	if claims["iss"] != b.APIURL() || claims["aud"] != a.cfg.WidgetURL() || jti == "" || consentID == "" || time.Unix(int64(exp), 0).Before(now) || (status != "Revoked" && status != "Expired") {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The event is not valid.")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.seenJTI == nil {
		a.seenJTI = map[string]time.Time{}
	}
	for k, until := range a.seenJTI {
		if until.Before(now) {
			delete(a.seenJTI, k)
		}
	}
	if _, dup := a.seenJTI[kid+"|"+jti]; dup {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	a.seenJTI[kid+"|"+jti] = time.Unix(int64(exp), 0)
	for _, c := range a.st.Connections {
		if c.ConsentID != consentID || c.Institution != kid {
			continue
		}
		if status == "Revoked" {
			a.setStatus(c, "revoked", "connection_revoked")
			a.vaultDrop(c.ID)
		} else {
			a.setStatus(c, "expired", "reauth_required")
		}
		a.poke()
	}
	w.WriteHeader(http.StatusNoContent)
}

func tlsRoots(pool *x509.CertPool) *tls.Config {
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}
