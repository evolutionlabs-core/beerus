package nibss

import (
	"context"
	"errors"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

var errNotBuilt = errors.New("not built")

type Store struct{}

var _ Switch = (*Store)(nil)

func Open(ctx context.Context, url, name string, outside []Institution) (*Store, error) {
	return nil, errNotBuilt
}

func (s *Store) Directory(ctx context.Context) ([]Institution, error) { return nil, errNotBuilt }

func (s *Store) Institution(ctx context.Context, code string) (Institution, error) {
	return Institution{}, errNotBuilt
}

func (s *Store) SetStatus(ctx context.Context, id, status string) error { return errNotBuilt }

func (s *Store) EnrolBVN(ctx context.Context, name, phone string, dob clock.Date) (string, error) {
	return "", errNotBuilt
}

func (s *Store) NameEnquiry(ctx context.Context, code, nuban string) (string, error) {
	return "", errNotBuilt
}

func (s *Store) Begin(ctx context.Context, x Session) (Session, error) { return Session{}, errNotBuilt }

func (s *Store) Resolve(ctx context.Context, id, status, outcome string, at time.Time) error {
	return errNotBuilt
}

func (s *Store) TSQ(ctx context.Context, id string) (Session, error) { return Session{}, errNotBuilt }

func (s *Store) Due(ctx context.Context, at time.Time) ([]Session, error) { return nil, errNotBuilt }

func (s *Store) Unsettled(ctx context.Context, upTo time.Time) ([]Session, error) {
	return nil, errNotBuilt
}

func (s *Store) MarkSettled(ctx context.Context, ids []string, session string) error {
	return errNotBuilt
}

func (s *Store) RecordConsent(ctx context.Context, e ConsentEvent) error { return errNotBuilt }

func (s *Store) ConsentLedger(ctx context.Context, bank string, limit int, cursor string) ([]ConsentEvent, string, error) {
	return nil, "", errNotBuilt
}
