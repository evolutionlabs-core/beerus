package server

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/charges"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/openbanking"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

const sweepEvery = time.Minute

type Step struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Done        bool       `json:"done"`
	Error       string     `json:"error,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type Server struct {
	id        string
	dir       string
	coreURL   string
	nibssPort int
	link      Link
	log       *slog.Logger
	clock     *clock.Clock
	fees      *charges.Schedule
	books     *bank.Bank
	banking   *Banking
	sender    *Sender
	idem      web.Idempotency
	faults    Faults
	phaseGen  atomic.Int64
	stale     atomic.Bool

	lifeMu sync.Mutex
	phase  string
	reason string
	steps  []Step

	mu      sync.Mutex
	bank    config.Bank
	worldID string
	replays map[string]*web.Record
	swept   time.Time

	regMu      sync.Mutex
	registry   map[string]*openbanking.RegistryClient
	regAt      time.Time
	regWarned  string
	regRefresh refresher

	dirMu      sync.Mutex
	dirBanks   []*config.Bank
	dirAt      time.Time
	dirClient  *http.Client
	dirRefresh refresher
}

type Config struct {
	Dir       string
	Bank      config.Bank
	Books     *bank.Bank
	Fees      *charges.Schedule
	NIBSSPort int
	CoreURL   string
	Link      Link
	Log       *slog.Logger
	Clock     *clock.Clock
	SetupKey  uint64
	Phase     string
	Steps     []Step
}

func New(c Config) *Server {
	s := &Server{
		id:        c.Bank.ID,
		dir:       c.Dir,
		coreURL:   c.CoreURL,
		nibssPort: c.NIBSSPort,
		link:      c.Link,
		log:       c.Log,
		clock:     c.Clock,
		fees:      c.Fees,
		books:     c.Books,
		phase:     c.Phase,
		steps:     c.Steps,
		bank:      c.Bank,
		worldID:   web.NewID("wld"),
		replays:   map[string]*web.Record{},
		registry:  map[string]*openbanking.RegistryClient{},
	}
	s.banking = &Banking{Src: s, Clock: c.Clock, Log: c.Log, ID: s.id}
	s.sender = NewSender(SenderConfig{
		Own:      s.Bank,
		Books:    func() *bank.Bank { return s.books },
		Fees:     c.Fees,
		Clock:    c.Clock,
		Log:      c.Log,
		SetupKey: c.SetupKey,
		Phase:    s.phaseOf,
		Lookup:   s.Lookup,
		Refresh:  s.RefreshBanks,
		NIBSS:    s.nibss,
	})
	return s
}

func (s *Server) ID() string {
	return s.id
}

func (s *Server) Banking() *Banking {
	return s.banking
}

func (s *Server) Sender() *Sender {
	return s.sender
}

func (s *Server) Bank() config.Bank {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bank
}

func (s *Server) Phase() (string, string) {
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	return s.phase, s.reason
}

func (s *Server) phaseOf(id string) string {
	if id != s.id {
		return config.PhaseActive
	}
	phase, _ := s.Phase()
	return phase
}

func (s *Server) WorldID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.worldID
}

func (s *Server) TakeStale() bool {
	return s.stale.Swap(false)
}

func (s *Server) MarkStale() {
	s.stale.Store(true)
}

func (s *Server) Refusing(id string) bool {
	if s.phaseOf(id) == config.PhaseSuspended {
		return true
	}
	return id == s.id && s.faults.Down(s.clock.Now())
}

func (s *Server) TakeTimeout(id string) (string, bool) {
	if id != s.id {
		return "", false
	}
	return s.faults.TakeTimeout()
}

func (s *Server) Sweep() {
	real := s.clock.Real()
	s.mu.Lock()
	defer s.mu.Unlock()
	if real.Sub(s.swept) < sweepEvery {
		return
	}
	s.swept = real
	for k, rec := range s.replays {
		if real.Sub(rec.CreatedReal) > web.IdempotencyTTL {
			delete(s.replays, k)
		}
	}
	s.faults.Prune(s.clock.Now())
}

func (s *Server) Book(id string) (Book, error) {
	if id != s.id {
		return Book{}, ErrBankNotFound
	}
	phase, _ := s.Phase()
	return Book{Name: s.Bank().Name, Phase: phase, Bank: s.books, Enrol: s.Enrol, Send: s.sender}, nil
}

func (s *Server) Empty() bool {
	return false
}

func (s *Server) Lookup(id string) *config.Bank {
	if id == s.id {
		own := s.Bank()
		return &own
	}
	for _, b := range s.Directory() {
		if b.ID == id {
			return b
		}
	}
	return nil
}

func (s *Server) Quote(ctx context.Context, req TransferRequest) (TransferResult, error) {
	return s.sender.Quote(ctx, req)
}

func (s *Server) Transfer(ctx context.Context, req TransferRequest) (TransferResult, error) {
	return s.sender.Transfer(ctx, req)
}
