package aggregator

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

const (
	seenLimit    = 10000
	maxEventBody = 16 << 10
)

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
	return a.bank(id)
}

func (a *App) consentEvents(w http.ResponseWriter, r *http.Request) {
	kid := r.Header.Get(config.CallerHeader)
	if kid == "" {
		web.WriteError(w, r, http.StatusUnauthorized, "unauthenticated", fmt.Sprintf("Send your ID in the %s header.", config.CallerHeader))
		return
	}
	b := a.bankConfig(kid)
	if b == nil {
		web.WriteError(w, r, http.StatusForbidden, "forbidden", fmt.Sprintf("%s is not a bank Connect knows.", kid))
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxEventBody+1))
	if err != nil || len(raw) > maxEventBody {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "The event could not be read.")
		return
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "The event is not JSON.")
		return
	}
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
