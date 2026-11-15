package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/evolutionlabs-core/beerus/internal/nip"
)

func (s *Server) CoreDo(ctx context.Context, method, path string, in, out any) (int, error) {
	return s.link.Do(ctx, s.coreURL, method, path, in, out)
}

func (s *Server) coreAway(err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	return &UnavailableError{Msg: s.Bank().Name + " cannot reach NIBSS right now. Try again in a minute.", Err: err}
}

func (s *Server) Transfer(ctx context.Context, req TransferRequest) (TransferResult, error) {
	body := TransferBody{From: req.From, To: req.To, ToBank: req.ToBank, Amount: fmt.Sprintf("%d.%02d", req.Amount/100, req.Amount%100), Narration: req.Narration}
	var out TransferResult
	status, err := s.CoreDo(ctx, http.MethodPost, "/banks/"+url.PathEscape(req.FromBank)+"/transfers", body, &out)
	var pe *PeerError
	if err != nil && !errors.As(err, &pe) {
		return out, s.coreAway(err)
	}
	if err != nil && status >= http.StatusBadRequest && status < http.StatusInternalServerError {
		return out, Refused(status, nip.SystemError, "transfer_refused", err)
	}
	return out, err
}
