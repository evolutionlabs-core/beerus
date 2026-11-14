package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/charges"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nip"
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
	reloaded  func(config.Bank)
	idem      web.Idempotency
	phaseGen  atomic.Int64
	stale     atomic.Bool

	lifeMu sync.Mutex
	phase  string
	reason string
	steps  []Step

	mu      sync.Mutex
	bank    config.Bank
	worldID string
	faults  []*Fault
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

func New(dir string, bank config.Bank, books *bank.Bank, fees *charges.Schedule, nibssPort int, coreURL string, link Link, log *slog.Logger, clk *clock.Clock, phase string, steps []Step, reloaded func(config.Bank)) *Server {
	s := &Server{
		id:        bank.ID,
		dir:       dir,
		coreURL:   coreURL,
		nibssPort: nibssPort,
		link:      link,
		log:       log,
		clock:     clk,
		fees:      fees,
		books:     books,
		reloaded:  reloaded,
		phase:     phase,
		steps:     steps,
		bank:      bank,
		worldID:   web.NewID("wld"),
		replays:   map[string]*web.Record{},
		registry:  map[string]*openbanking.RegistryClient{},
	}
	s.banking = &Banking{Src: s, Clock: clk, Log: log, ID: s.id}
	return s
}

func (s *Server) ID() string {
	return s.id
}

func (s *Server) Banking() *Banking {
	return s.banking
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
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.downLocked(id, s.clock.Now())
}

func (s *Server) downLocked(id string, at time.Time) bool {
	return slices.ContainsFunc(s.faults, func(f *Fault) bool { return f.Kind == FaultDown && f.Bank == id && f.Live(at) })
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
	s.pruneFaultsLocked(s.clock.Now())
}

func (s *Server) Book(id string) (Book, error) {
	if id != s.id {
		return Book{}, ErrBankNotFound
	}
	phase, _ := s.Phase()
	return Book{Name: s.Bank().Name, Phase: phase, Bank: s.books}, nil
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
	if err := CheckRequest(req); err != nil {
		return TransferResult{}, err
	}
	if req.ToBank == "" {
		req.ToBank = req.FromBank
	}
	from, to := s.Lookup(req.FromBank), s.Lookup(req.ToBank)
	var fromPhase, toPhase string
	if from != nil {
		fromPhase = s.phaseOf(from.ID)
	}
	if to != nil {
		toPhase = s.phaseOf(to.ID)
	}
	rt, err := NewRoute(req, from, to, fromPhase, toPhase)
	if err != nil {
		return TransferResult{}, err
	}
	if rt.FromBank != s.id {
		return TransferResult{}, Refused(http.StatusServiceUnavailable, nip.Unavailable, "bank_unavailable", fmt.Errorf("the books of %s are not open yet", rt.FromName))
	}
	return Quoter{Fees: s.fees, Clock: s.clock, Enquirer: s}.Quote(ctx, s.books, rt, req)
}
