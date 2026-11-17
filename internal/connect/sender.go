package connect

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/nip"
)

const sendsEvery = time.Second

func (a *App) bankSender(id string) *server.Sender {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s := a.senders[id]; s != nil {
		return s
	}
	if a.senders == nil {
		a.senders = map[string]*server.Sender{}
	}
	s := server.NewSender(server.SenderConfig{
		Own: func() config.Bank {
			b, _ := a.bankCopy(id)
			return b
		},
		Books: func() *bank.Bank {
			if l := a.obLedger(id); l != nil {
				return l.Bank()
			}
			return nil
		},
		Fees:     a.cfg.Fees(),
		Clock:    a.clock,
		Log:      a.log,
		SetupKey: a.setupKey,
		Phase:    a.bankPhase,
		Lookup: func(other string) *config.Bank {
			b, ok := a.bankCopy(other)
			if !ok {
				return nil
			}
			return &b
		},
		NIBSS: func() (server.NIBSSClient, error) { return a.bankNIBSS(id) },
	})
	a.senders[id] = s
	return s
}

func (a *App) dropBankState(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.senders, id)
	delete(a.faults, id)
}

func (a *App) runSends(ctx context.Context) {
	tick := time.NewTicker(sendsEvery)
	defer tick.Stop()
	for {
		for _, b := range a.bankList() {
			if err := a.bankSender(b.ID).Tick(ctx); err != nil && ctx.Err() == nil {
				a.log.Error("sends not checked", "bank", b.ID, "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (a *App) transfer(ctx context.Context, req server.TransferRequest) (server.TransferResult, error) {
	if a.role != RoleCore {
		return a.bankSender(req.FromBank).Transfer(ctx, req)
	}
	b, ok := a.bankCopy(req.FromBank)
	if !ok {
		return server.TransferResult{}, server.Refused(http.StatusNotFound, nip.SystemError, "not_found", fmt.Errorf("no bank %q", req.FromBank))
	}
	body := server.TransferBody{From: req.From, ToBank: req.ToBank, To: req.To, Amount: fmt.Sprintf("%d.%02d", req.Amount/100, req.Amount%100), Narration: req.Narration, Closure: req.Closure}
	header := http.Header{}
	if req.Key != "" {
		header.Set("Idempotency-Key", req.Key)
	}
	var out server.TransferResult
	_, err := a.link.Request(ctx, b.URL(), http.MethodPost, "/v1/transfers", header, body, &out)
	var peer *server.PeerError
	if errors.As(err, &peer) {
		return out, server.Refused(peer.Status, nip.SystemError, peer.Code, errors.New(peer.Message))
	}
	return out, err
}

func (a *App) Session(ctx context.Context, id string) (nibss.Session, error) {
	sw := a.nibssStore()
	if sw == nil {
		return nibss.Session{}, errors.New("NIBSS is not available right now")
	}
	return sw.Session(ctx, strings.TrimSpace(id))
}

func (a *App) QuerySession(ctx context.Context, id string) (server.NIPSession, error) {
	x, err := a.Session(ctx, id)
	if err != nil {
		return server.NIPSession{}, err
	}
	var from *config.Bank
	for _, b := range a.bankList() {
		if b.NIPCode == x.FromCode {
			from = b
		}
	}
	if from == nil {
		return server.NIPSession{}, fmt.Errorf("session %s: the sending bank %s is not in this setup", x.ID, x.FromCode)
	}
	if a.role != RoleCore {
		sess, err := a.bankSender(from.ID).StatusQuery(ctx, x.ID)
		return sess, queryFailed(x.ID, err)
	}
	var sess server.NIPSession
	_, err = a.link.Request(ctx, from.URL(), http.MethodPost, "/v1/nip/sends/"+url.PathEscape(x.ID)+"/status-query", nil, nil, &sess)
	return sess, queryFailed(x.ID, err)
}

func queryFailed(id string, err error) error {
	var peer *server.PeerError
	switch {
	case errors.Is(err, server.ErrNoSend):
		return fmt.Errorf("session %s: %w", id, nip.ErrNoRecord)
	case errors.As(err, &peer) && peer.Status == http.StatusNotFound:
		return fmt.Errorf("session %s: %w", id, nip.ErrNoRecord)
	case err != nil:
		return fmt.Errorf("query session %s: %w", id, err)
	}
	return nil
}
