package consent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

const (
	eventBatch       = 50
	eventLife        = 5 * time.Minute
	eventIdleTimeout = 90 * time.Second
	eventTimeout     = 5 * time.Second
)

type Sender struct {
	Log       *slog.Logger
	Clock     *clock.Clock
	WidgetURL string
	Servers   func() map[string]*openbanking.Server
	Bank      func(id string) *config.Bank
	Record    func(ctx context.Context, incarnation string, e openbanking.ConsentEvent) error
	Report    func(bank string, problems []string)

	flushMu sync.Mutex
	warned  map[string]string
	mu      sync.Mutex
	client  *http.Client
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
			s.warnOnce(id+"/events", "consent events not read", id, err)
			continue
		}
		for _, e := range due {
			if err := s.send(ctx, b, e); err != nil {
				s.warnOnce(id+"/event", "consent event not delivered; will retry", id, err, "consent", e.ConsentID)
				if err := srv.Retry(context.WithoutCancel(ctx), e, s.Clock.Real().Add(openbanking.Backoff(e.Attempts))); err != nil {
					s.Log.Error("consent event retry not saved", "bank", id, "consent", e.ConsentID, "err", err)
				}
				continue
			}
			s.cleared(id + "/event")
			if err := srv.Delivered(context.WithoutCancel(ctx), e); err != nil {
				s.Log.Error("consent event delivery not saved", "bank", id, "consent", e.ConsentID, "err", err)
			}
		}
	}
}

func (s *Sender) Ledger(ctx context.Context) {
	if s.Record == nil {
		return
	}
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	for id, srv := range s.Servers() {
		s.flushLedger(ctx, id, srv)
	}
}

func (s *Sender) flushLedger(ctx context.Context, id string, srv *openbanking.Server) {
	incarnation, err := srv.Incarnation(ctx)
	if err != nil {
		s.warnOnce(id+"/incarnation", "consent ledger incarnation not read", id, err)
		return
	}
	due, err := srv.LedgerDue(ctx, eventBatch)
	if err != nil {
		s.warnOnce(id+"/ledger-read", "consent ledger events not read", id, err)
		return
	}
	save := context.WithoutCancel(ctx)
	for _, e := range due {
		ev := e.ConsentEvent
		ev.Seq = e.ID
		err := s.Record(ctx, incarnation, ev)
		if err == nil {
			s.cleared(id + "/ledger")
			if err := srv.Delivered(save, e); err != nil {
				s.Log.Error("consent ledger delivery not saved", "bank", id, "consent", e.ConsentID, "err", err)
				break
			}
			continue
		}
		if reason, ok := refused(err); ok {
			if err := srv.RejectLedger(save, e, reason); err != nil {
				s.Log.Error("consent ledger rejection not saved", "bank", id, "consent", e.ConsentID, "err", err)
				break
			}
			continue
		}
		s.warnOnce(id+"/ledger", "consent ledger not updated; will retry", id, err, "consent", e.ConsentID)
		if err := srv.Retry(save, e, s.Clock.Real().Add(openbanking.Backoff(e.Attempts))); err != nil {
			s.Log.Error("consent ledger retry not saved", "bank", id, "consent", e.ConsentID, "err", err)
		}
		break
	}
	s.report(ctx, id, srv)
}

func (s *Sender) report(ctx context.Context, id string, srv *openbanking.Server) {
	if s.Report == nil {
		return
	}
	held, err := srv.LedgerRejected(ctx)
	if err != nil {
		s.warnOnce(id+"/rejected-read", "rejected consent events not read", id, err)
		return
	}
	problems := make([]string, 0, len(held))
	for _, r := range held {
		problems = append(problems, fmt.Sprintf("NIBSS refused the %s event of consent %s and it will not be sent again: %s", r.Status, r.ConsentID, r.Reason))
	}
	s.Report(id, problems)
}

func refused(err error) (string, bool) {
	var peer *control.PeerError
	if errors.As(err, &peer) && (peer.Status == http.StatusBadRequest || peer.Status == http.StatusUnprocessableEntity) {
		return peer.Message, true
	}
	return "", false
}

func (s *Sender) warnOnce(key, msg, bank string, err error, more ...any) {
	if s.warned == nil {
		s.warned = map[string]string{}
	}
	if s.warned[key] == err.Error() {
		return
	}
	s.warned[key] = err.Error()
	s.Log.Warn(msg, append([]any{"bank", bank, "err", err}, more...)...)
}

func (s *Sender) cleared(key string) {
	delete(s.warned, key)
}

func (s *Sender) httpClient() *http.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client == nil {
		s.client = &http.Client{Timeout: eventTimeout, Transport: &http.Transport{MaxIdleConnsPerHost: 2, IdleConnTimeout: eventIdleTimeout}}
	}
	return s.client
}

func (s *Sender) send(ctx context.Context, b *config.Bank, e openbanking.Event) error {
	now := s.Clock.Real()
	jti, err := web.NewSecret("ev_", 12)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{
		"iss":        b.APIURL(),
		"aud":        s.WidgetURL,
		"jti":        jti,
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.WidgetURL+"/consent-events", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(config.CallerHeader, b.ID)
	resp, err := s.httpClient().Do(req)
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
