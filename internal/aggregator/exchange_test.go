package aggregator

import (
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

func TestExchangeRefusesBadPublicTokens(t *testing.T) {
	partner := &config.Partner{ID: "p1"}
	a := &App{
		cfg:   &config.Config{Partners: []*config.Partner{partner}},
		st:    newState(),
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		clock: clock.New(),
	}
	now := a.clock.Real()
	a.st.LinkSessions["ls_expired"] = &LinkSession{ID: "ls_expired", PartnerID: "p1", Status: "authorised", PublicHash: web.HashSecret("public-old"), PublicExpires: now.Add(-time.Minute)}
	a.st.LinkSessions["ls_used"] = &LinkSession{ID: "ls_used", PartnerID: "p1", Status: "exchanged", PublicUsed: true, PublicHash: web.HashSecret("public-used"), PublicExpires: now.Add(time.Minute)}
	a.st.LinkSessions["ls_other"] = &LinkSession{ID: "ls_other", PartnerID: "p2", Status: "authorised", PublicHash: web.HashSecret("public-other"), PublicExpires: now.Add(time.Minute)}
	rows := []struct {
		name, body, code, message string
		status                    int
	}{
		{"no token", `{}`, "invalid_request", "The body must be a JSON object with public_token.", http.StatusBadRequest},
		{"unknown", `{"public_token":"public-nope"}`, "invalid_public_token", "This public_token is not known.", http.StatusNotFound},
		{"another partner's", `{"public_token":"public-other"}`, "invalid_public_token", "This public_token is not known.", http.StatusNotFound},
		{"expired", `{"public_token":"public-old"}`, "invalid_public_token", "This public_token has expired. Start a new link.", http.StatusNotFound},
		{"used", `{"public_token":"public-used"}`, "invalid_public_token", "This public_token was already used.", http.StatusNotFound},
	}
	for _, c := range rows {
		status, body := callPartner(t, partner, a.exchange, http.MethodPost, "/v1/public_token/exchange", c.body, nil)
		apiErr, _ := body["error"].(map[string]any)
		if status != c.status || apiErr["code"] != c.code || apiErr["message"] != c.message {
			t.Errorf("%s: %d %v, want %d %s %q", c.name, status, body, c.status, c.code, c.message)
		}
	}
}
