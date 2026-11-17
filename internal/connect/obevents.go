package connect

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/pki"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

const seenLimit = 10000

type jtiSet struct {
	mu    sync.Mutex
	seen  map[string]time.Time
	swept time.Time
}

var errTooManyEvents = errors.New("too many events")

func (j *jtiSet) first(key string, until, now time.Time) (bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.seen == nil {
		j.seen = map[string]time.Time{}
	}
	if now.Sub(j.swept) >= time.Minute || len(j.seen) >= seenLimit {
		for k, exp := range j.seen {
			if exp.Before(now) {
				delete(j.seen, k)
			}
		}
		j.swept = now
	}
	if _, dup := j.seen[key]; dup {
		return false, nil
	}
	if len(j.seen) >= seenLimit {
		return false, errTooManyEvents
	}
	j.seen[key] = until
	return true, nil
}

func (a *App) bankConfig(id string) *config.Bank {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.Bank(id)
}

func (a *App) bankVerifyKey(id string) (*ecdsa.PublicKey, error) {
	if a.bankConfig(id) == nil {
		return nil, errors.New("unknown bank")
	}
	raw, err := os.ReadFile(a.cfg.Path("banks", id, config.EventSignerName+".crt"))
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("the bank's event certificate is malformed")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	pool, err := pki.LoadPool(a.cfg.Path("ca", "root.crt"))
	if err != nil {
		return nil, err
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, CurrentTime: a.clock.Real(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		return nil, err
	}
	if cert.Subject.CommonName != id+"-events" || !slices.Contains(cert.Subject.OrganizationalUnit, config.EventSignerRole) || cert.KeyUsage&x509.KeyUsageDigitalSignature == 0 || len(cert.ExtKeyUsage) > 0 {
		return nil, errors.New("the certificate is not this bank's event signer")
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("the bank's event key is not an EC key")
	}
	return pub, nil
}

func (a *App) consentEvents(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 16<<10))
	if err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "The event could not be read.")
		return
	}
	token := string(bytes.TrimSpace(raw))
	head, _, err := openbanking.Peek(token)
	kid, _ := head["kid"].(string)
	if err != nil || kid == "" {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "The event is not a signed token.")
		return
	}
	pub, err := a.bankVerifyKey(kid)
	if err != nil {
		web.WriteError(w, r, http.StatusUnauthorized, "invalid_signature", "The bank's key could not be checked.")
		return
	}
	claims, err := openbanking.Verify(token, []*ecdsa.PublicKey{pub})
	if err != nil {
		web.WriteError(w, r, http.StatusUnauthorized, "invalid_signature", "The signature does not match the bank.")
		return
	}
	b := a.bankConfig(kid)
	exp, _ := claims["exp"].(float64)
	jti, _ := claims["jti"].(string)
	consentID, _ := claims["consent_id"].(string)
	status, _ := claims["status"].(string)
	now := a.clock.Real()
	if claims["iss"] != b.APIURL() || claims["aud"] != a.cfg.WidgetURL() || jti == "" || consentID == "" || time.Unix(int64(exp), 0).Before(now) || !knownStatus(status) {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "The event is not valid.")
		return
	}
	fresh, err := a.eventJTI.first(kid+"|"+jti, time.Unix(int64(exp), 0), now)
	if err != nil {
		web.WriteError(w, r, http.StatusTooManyRequests, "rate_limited", "Too many events. Try again shortly.")
		return
	}
	if !fresh {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range a.st.Connections {
		if c.ConsentID != consentID || c.Institution != kid {
			continue
		}
		switch openbanking.Status(status) {
		case openbanking.Revoked:
			a.setStatus(c, "revoked", "connection_revoked")
			a.vaultDrop(c.ID)
		case openbanking.Expired, openbanking.Dormant, openbanking.ReconsentRequired:
			a.setStatus(c, "expired", "reauth_required")
		}
		a.poke()
	}
	w.WriteHeader(http.StatusNoContent)
}

func knownStatus(s string) bool {
	switch openbanking.Status(s) {
	case openbanking.Authorised, openbanking.Rejected, openbanking.Revoked, openbanking.Expired, openbanking.Dormant, openbanking.ReconsentRequired:
		return true
	}
	return false
}
