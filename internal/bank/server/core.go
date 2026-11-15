package server

import (
	"context"
	"errors"
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
