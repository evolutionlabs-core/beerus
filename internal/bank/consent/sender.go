package consent

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/pki"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

const (
	eventType        = "application/secevent+jwt"
	eventBackoff     = 2 * time.Second
	eventBackoffAt   = 10 * time.Minute
	eventBatch       = 50
	eventLife        = 5 * time.Minute
	eventIdleTimeout = 90 * time.Second
	eventTimeout     = 5 * time.Second
)

type Sender struct {
	Log       *slog.Logger
	Clock     *clock.Clock
	Dir       string
	WidgetURL string
	CA        string
	Servers   func() map[string]*openbanking.Server
	Bank      func(id string) *config.Bank
	Core      func(ctx context.Context, e openbanking.ConsentEvent) error

	flushMu sync.Mutex
	mu      sync.Mutex
	clients map[string]*http.Client
}

func Backoff(attempts int) time.Duration {
	if attempts >= 20 {
		return eventBackoffAt
	}
	return min(eventBackoff<<attempts, eventBackoffAt)
}

func (s *Sender) Events(ctx context.Context) {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	for id, srv := range s.Servers() {
		b := s.Bank(id)
		if b == nil {
			continue
		}
		due, err := srv.Due(ctx, eventBatch)
		if err != nil {
			s.Log.Warn("consent events not read", "bank", id, "err", err)
			continue
		}
		for _, e := range due {
			if err := s.send(ctx, b, e); err != nil {
				s.Log.Warn("consent event not delivered; will retry", "bank", id, "consent", e.ConsentID, "attempt", e.Attempts+1, "err", err)
				if err := srv.Retry(ctx, e, s.Clock.Real().Add(Backoff(e.Attempts))); err != nil {
					s.Log.Error("consent event retry not saved", "bank", id, "consent", e.ConsentID, "err", err)
				}
				continue
			}
			if err := srv.Delivered(ctx, e); err != nil {
				s.Log.Error("consent event delivery not saved", "bank", id, "consent", e.ConsentID, "err", err)
			}
		}
	}
}

func (s *Sender) Ledger(ctx context.Context) {
	if s.Core == nil {
		return
	}
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	for id, srv := range s.Servers() {
		due, err := srv.LedgerDue(ctx, eventBatch)
		if err != nil {
			s.Log.Warn("consent ledger events not read", "bank", id, "err", err)
			continue
		}
		for _, e := range due {
			ev := e.ConsentEvent
			ev.Seq = e.ID
			if err := s.Core(ctx, ev); err != nil {
				s.Log.Warn("consent ledger not updated; will retry", "bank", id, "consent", e.ConsentID, "attempt", e.Attempts+1, "err", err)
				if err := srv.Retry(ctx, e, s.Clock.Real().Add(Backoff(e.Attempts))); err != nil {
					s.Log.Error("consent ledger retry not saved", "bank", id, "consent", e.ConsentID, "err", err)
				}
				break
			}
			if err := srv.Delivered(ctx, e); err != nil {
				s.Log.Error("consent ledger delivery not saved", "bank", id, "consent", e.ConsentID, "err", err)
				break
			}
		}
	}
}

func (s *Sender) signer(b *config.Bank) (*ecdsa.PrivateKey, error) {
	raw, err := os.ReadFile(filepath.Join(s.Dir, "banks", b.ID, config.EventSignerName+".key"))
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("the bank's event key is malformed")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("the bank's event key is not an EC key")
	}
	return key, nil
}

func (s *Sender) client(dest string) (*http.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cl := s.clients[dest]; cl != nil {
		return cl, nil
	}
	pool, err := pki.LoadPool(s.CA)
	if err != nil {
		return nil, err
	}
	cl := &http.Client{Timeout: eventTimeout, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, MaxIdleConnsPerHost: 2, IdleConnTimeout: eventIdleTimeout}}
	if s.clients == nil {
		s.clients = map[string]*http.Client{}
	}
	s.clients[dest] = cl
	return cl, nil
}

func (s *Sender) send(ctx context.Context, b *config.Bank, e openbanking.Event) error {
	key, err := s.signer(b)
	if err != nil {
		return err
	}
	now := s.Clock.Real()
	jwt, err := openbanking.Sign(key, b.ID, map[string]any{
		"iss":        b.APIURL(),
		"aud":        s.WidgetURL,
		"jti":        web.NewSecret("ev_", 12),
		"iat":        now.Unix(),
		"exp":        now.Add(eventLife).Unix(),
		"consent_id": e.ConsentID,
		"status":     string(e.Status),
		"via":        e.Via,
		"at":         e.At.Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	cl, err := s.client(s.WidgetURL)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.WidgetURL+"/consent-events", bytes.NewReader([]byte(jwt)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", eventType)
	resp, err := cl.Do(req)
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
