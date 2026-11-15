package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/clock"
)

func (s *Sender) Run(ctx context.Context) {
	tick := time.NewTicker(sendPoll)
	defer tick.Stop()
	for {
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			s.Log.Error("sends not checked", "bank", s.Own().ID, "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (s *Sender) Tick(ctx context.Context) error {
	b := s.Books()
	if b == nil {
		return nil
	}
	due, err := b.DueSends(ctx, s.Clock.Real(), sendBatch)
	if err != nil {
		return err
	}
	for _, row := range due {
		if _, err := s.check(ctx, b, row); err != nil && ctx.Err() == nil {
			s.Log.Warn("send not checked; retrying", "bank", s.Own().ID, "session", row.SessionID, "err", err)
		}
	}
	return nil
}

func (s *Sender) check(ctx context.Context, b *bank.Bank, row bank.Send) (NIPSession, error) {
	sess, err := s.ask(ctx, row.SessionID, row.Request)
	if errors.Is(err, ErrSendRefused) {
		if rerr := s.undo(ctx, b, row.SessionID); rerr != nil {
			return NIPSession{}, errors.Join(err, rerr)
		}
		return NIPSession{}, fmt.Errorf("reverse session %s: %w", row.SessionID, err)
	}
	if err != nil {
		if rerr := b.RescheduleSend(ctx, row.SessionID, s.Clock.Real().Add(clock.Backoff(row.Attempts+1))); rerr != nil {
			return NIPSession{}, errors.Join(err, rerr)
		}
		return NIPSession{}, err
	}
	res := TransferResult{}
	return sess, s.conclude(ctx, b, row.SessionID, sess, row.Attempts, &res)
}

func (s *Sender) ask(ctx context.Context, sid string, request json.RawMessage) (NIPSession, error) {
	c, err := s.NIBSS()
	if err != nil {
		return NIPSession{}, err
	}
	var sess NIPSession
	_, err = c.Do(ctx, http.MethodPost, transfersPath+"/"+url.PathEscape(sid)+"/status-queries", nil, nil, &sess)
	var peer *PeerError
	if !errors.As(err, &peer) || peer.Status != http.StatusNotFound || len(request) == 0 {
		return sess, err
	}
	_, err = c.Do(ctx, http.MethodPost, transfersPath, nil, request, &sess)
	if errors.As(err, &peer) && peer.Status < http.StatusInternalServerError {
		return sess, errors.Join(ErrSendRefused, err)
	}
	return sess, err
}

func (s *Sender) StatusQuery(ctx context.Context, sid string) (NIPSession, error) {
	b := s.Books()
	if b == nil {
		return NIPSession{}, fmt.Errorf("the books of %s are not open yet", s.Own().Name)
	}
	row, open, err := b.OpenSend(ctx, sid)
	if err != nil {
		return NIPSession{}, err
	}
	if open {
		return s.check(ctx, b, row)
	}
	sess, err := s.ask(ctx, sid, nil)
	var gone *PeerError
	if errors.As(err, &gone) && gone.Status == http.StatusNotFound {
		return NIPSession{}, fmt.Errorf("%s has no send %s: %w", s.Own().Name, sid, ErrNoSend)
	}
	return sess, err
}
