package site

import (
	"context"
	"errors"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
)

type brokenStore struct {
	*openbanking.MemStore
	err error
}

func (b brokenStore) Consent(ctx context.Context, id string) (*openbanking.Consent, error) {
	if b.err != nil {
		return nil, b.err
	}
	return b.MemStore.Consent(ctx, id)
}

func TestInternalRevokeAnswers(t *testing.T) {
	const token = "link-token"
	rows := []struct {
		name  string
		err   error
		token string
		want  int
	}{
		{"an unknown consent is 404", nil, token, http.StatusNotFound},
		{"a store that fails is 500", errors.New("database down"), token, http.StatusInternalServerError},
		{"no token is 401", nil, "", http.StatusUnauthorized},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			open := openbanking.New(openbanking.Config{Store: brokenStore{MemStore: openbanking.NewMemStore(), err: r.err}})
			h := New(Config{Host: fakeHost{phase: config.PhaseActive}, Open: open, Link: control.NewLink(token, ""), Clock: clock.New(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
			req, err := http.NewRequest(http.MethodPost, "http://bank-a.localhost:7000/internal/revoke", strings.NewReader(`{"consent_id":"con_x"}`))
			if err != nil {
				t.Fatal(err)
			}
			req.RemoteAddr = "127.0.0.1:50000"
			req.Header.Set("Content-Type", "application/json")
			if r.token != "" {
				req.Header.Set("Authorization", "Bearer "+r.token)
			}
			c := &capture{header: http.Header{}}
			h.ServeHTTP(c, req)
			if c.status != r.want {
				t.Fatalf("answered %d, want %d: %s", c.status, r.want, c.body.String())
			}
		})
	}
}
