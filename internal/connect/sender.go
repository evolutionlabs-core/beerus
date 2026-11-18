package connect

import (
	"context"
	"errors"
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"net/http"
	"net/url"
	"strings"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/nip"
)

func (a *App) transfer(ctx context.Context, req control.TransferRequest) (control.TransferResult, error) {
	b, ok := a.bankCopy(req.FromBank)
	if !ok {
		return control.TransferResult{}, control.Refused(http.StatusNotFound, nip.SystemError, "not_found", fmt.Errorf("no bank %q", req.FromBank))
	}
	body := control.TransferBody{From: req.From, ToBank: req.ToBank, To: req.To, Amount: fmt.Sprintf("%d.%02d", req.Amount/100, req.Amount%100), Narration: req.Narration, Closure: req.Closure}
	header := http.Header{}
	if req.Key != "" {
		header.Set("Idempotency-Key", req.Key)
	}
	var out control.TransferResult
	_, err := a.link.Request(ctx, b.URL(), http.MethodPost, "/v1/transfers", header, body, &out)
	var peer *control.PeerError
	if errors.As(err, &peer) {
		return out, control.Refused(peer.Status, nip.SystemError, peer.Code, errors.New(peer.Message))
	}
	return out, err
}

func (a *App) Session(ctx context.Context, id string) (nibss.Session, error) {
	return a.nibssCtl.session(ctx, strings.TrimSpace(id))
}

func (a *App) QuerySession(ctx context.Context, id string) (control.NIPSession, error) {
	x, err := a.Session(ctx, id)
	if err != nil {
		return control.NIPSession{}, err
	}
	var from *config.Bank
	for _, b := range a.bankList() {
		if b.NIPCode == x.FromCode {
			from = b
		}
	}
	if from == nil {
		return control.NIPSession{}, fmt.Errorf("session %s: the sending bank %s is not in this setup", x.ID, x.FromCode)
	}
	var sess control.NIPSession
	_, err = a.link.Request(ctx, from.URL(), http.MethodPost, "/v1/nip/sends/"+url.PathEscape(x.ID)+"/status-query", nil, nil, &sess)
	return sess, queryFailed(x.ID, err)
}

func queryFailed(id string, err error) error {
	var peer *control.PeerError
	switch {
	case errors.Is(err, control.ErrNoSend):
		return fmt.Errorf("session %s: %w", id, nip.ErrNoRecord)
	case errors.As(err, &peer) && peer.Status == http.StatusNotFound:
		return fmt.Errorf("session %s: %w", id, nip.ErrNoRecord)
	case err != nil:
		return fmt.Errorf("query session %s: %w", id, err)
	}
	return nil
}
