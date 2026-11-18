package connect

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/evolutionlabs-core/beerus/internal/bank/server"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/nip"
)

func (a *App) transfer(ctx context.Context, req server.TransferRequest) (server.TransferResult, error) {
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
	return a.nibssCtl.session(ctx, strings.TrimSpace(id))
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
