package openbanking

import (
	"context"
	"slices"
	"sync"
	"time"
)

type MemStore struct {
	mu       sync.Mutex
	consents map[string]*Consent
	tokens   map[string]*Token
	access   []Access
	customer map[string]string
	events   []*outboxRow
}

type outboxRow struct {
	Event
	next      time.Time
	delivered bool
}

func NewMemStore() *MemStore {
	return &MemStore{consents: map[string]*Consent{}, tokens: map[string]*Token{}, customer: map[string]string{}}
}

func cloneConsent(c *Consent) *Consent {
	x := *c
	x.Accounts = slices.Clone(c.Accounts)
	x.Scopes = slices.Clone(c.Scopes)
	return &x
}

func (m *MemStore) CreateConsent(_ context.Context, c *Consent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.consents[c.ID] = cloneConsent(c)
	return nil
}

func (m *MemStore) Consent(_ context.Context, id string) (*Consent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.consents[id]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneConsent(c), nil
}

func (m *MemStore) UpdateConsent(_ context.Context, c *Consent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.consents[c.ID]; !ok {
		return ErrNotFound
	}
	m.consents[c.ID] = cloneConsent(c)
	return nil
}

func (m *MemStore) ConsentsFor(_ context.Context, customer string) ([]*Consent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*Consent{}
	for _, c := range m.consents {
		if c.Customer == customer {
			out = append(out, cloneConsent(c))
		}
	}
	slices.SortFunc(out, func(a, b *Consent) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return out, nil
}

func (m *MemStore) PutToken(_ context.Context, t *Token) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	x := *t
	m.tokens[t.Hash] = &x
	return nil
}

func (m *MemStore) Token(_ context.Context, hash string) (*Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tokens[hash]
	if !ok {
		return nil, ErrNotFound
	}
	x := *t
	return &x, nil
}

func (m *MemStore) MarkRotated(_ context.Context, hash string, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tokens[hash]
	if !ok || !t.RotatedAt.IsZero() {
		return false, nil
	}
	t.RotatedAt = at
	return true, nil
}

func (m *MemStore) revoke(match func(*Token) bool) {
	for _, t := range m.tokens {
		if match(t) {
			t.Revoked = true
		}
	}
}

func (m *MemStore) RevokeConsentTokens(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.revoke(func(t *Token) bool { return t.ConsentID == id })
	return nil
}

func (m *MemStore) RevokeChildren(_ context.Context, parent string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.revoke(func(t *Token) bool { return t.Parent == parent })
	return nil
}

func (m *MemStore) RevokeFamily(_ context.Context, family string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.revoke(func(t *Token) bool { return t.Family == family })
	return nil
}

func (m *MemStore) RecordAccess(_ context.Context, a Access) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.access = append(m.access, a)
	return nil
}

func (m *MemStore) AccessFor(_ context.Context, customer string, limit int) ([]Access, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Access{}
	for i := len(m.access) - 1; i >= 0 && len(out) < limit; i-- {
		if c, ok := m.consents[m.access[i].ConsentID]; ok && c.Customer == customer {
			out = append(out, m.access[i])
		}
	}
	return out, nil
}

func (m *MemStore) Ping(context.Context) error {
	return nil
}

func (m *MemStore) MarkUsed(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.consents[id]; ok {
		c.LastUsedAt = at
	}
	return nil
}

func (m *MemStore) Awaiting(context.Context) ([]*Consent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*Consent{}
	for _, c := range m.consents {
		if c.Status == AwaitingAuthorisation {
			out = append(out, cloneConsent(c))
		}
	}
	return out, nil
}

func (m *MemStore) Enqueue(_ context.Context, e ConsentEvent, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, &outboxRow{Event: Event{ConsentEvent: e, ID: int64(len(m.events) + 1)}, next: at})
	return nil
}

func (m *MemStore) Due(_ context.Context, now time.Time, limit int) ([]Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Event{}
	for _, r := range m.events {
		if !r.delivered && !r.next.After(now) && len(out) < limit {
			out = append(out, r.Event)
		}
	}
	return out, nil
}

func (m *MemStore) Delivered(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events[id-1].delivered = true
	return nil
}

func (m *MemStore) Retry(_ context.Context, id int64, attempts int, next time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events[id-1].Attempts, m.events[id-1].next = attempts, next
	return nil
}
