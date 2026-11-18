package server

import (
	"fmt"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

const (
	booksNotReady    = "The bank database is not ready yet. Try again soon."
	participantKind  = "bank"
	faultPageDefault = 50
)

func validPhase(p string) bool {
	switch p {
	case config.PhaseProvisioning, config.PhaseActive, config.PhaseSuspended, config.PhaseRepairing, config.PhaseClosing, config.PhaseClosed, config.PhaseTerminating:
		return true
	}
	return false
}

type replayStore struct {
	s *Server
}

func (r replayStore) Lookup(scope string) *web.Record {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	rec := r.s.replays[scope]
	if rec != nil && r.s.clock.Real().Sub(rec.CreatedReal) > web.IdempotencyTTL {
		delete(r.s.replays, scope)
		return nil
	}
	return rec
}

func (r replayStore) Commit(scope string, rec *web.Record) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if rec != nil {
		rec.CreatedReal = r.s.clock.Real()
		r.s.replays[scope] = rec
	}
	return nil
}

func viewFault(f control.Fault) control.FaultView {
	v := control.FaultView{ID: f.ID, Kind: f.Kind, Outcome: f.Outcome, Left: f.Left, CreatedAt: f.At.In(clock.Lagos)}
	if f.Until != nil {
		until := f.Until.In(clock.Lagos)
		v.UntilAt = &until
	}
	return v
}

func (s *Server) API() http.Handler {
	owner := func(*http.Request) string { return web.ControlOwner }
	idempotent := func(next http.HandlerFunc) http.HandlerFunc {
		return s.idem.Wrap(replayStore{s}, owner, s.log, next)
	}
	booked := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if s.books.Load() == nil {
				web.WriteError(w, r, http.StatusServiceUnavailable, "unavailable", booksNotReady)
				return
			}
			next(w, r)
		}
	}
	write := func(next http.HandlerFunc) http.HandlerFunc {
		return idempotent(booked(next))
	}
	b := s.banking
	closing := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if _, err := b.closingBook(s.id); err != nil {
				b.Error(w, r, err)
				return
			}
			next(w, r)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.health)
	mux.HandleFunc("GET /v1/status", s.status)
	mux.HandleFunc("PUT /v1/phase", s.setPhase)
	mux.HandleFunc("GET /v1/faults", s.listFaults)
	mux.HandleFunc("POST /v1/faults", idempotent(s.setFault))
	mux.HandleFunc("DELETE /v1/faults", s.clearFaults)
	mux.HandleFunc("GET /v1/customers", booked(b.HandleCustomers))
	mux.HandleFunc("POST /v1/customers", write(b.HandleCreateCustomer))
	mux.HandleFunc("POST /v1/customers/{customer_id}/close", write(b.HandleCloseCustomer))
	mux.HandleFunc("GET /v1/accounts", booked(b.HandleAccounts))
	mux.HandleFunc("POST /v1/accounts", write(b.HandleOpenAccount))
	mux.HandleFunc("GET /v1/accounts/{nuban}", booked(b.HandleAccount))
	mux.HandleFunc("POST /v1/accounts/{nuban}/close", write(b.HandleCloseAccount))
	mux.HandleFunc("POST /v1/accounts/{nuban}/freeze", booked(closing(write(b.HandleFreeze))))
	mux.HandleFunc("GET /v1/accounts/{nuban}/statement", booked(b.HandleStatement))
	mux.HandleFunc("POST /v1/transfers", write(b.HandleTransfer))
	mux.HandleFunc("POST /v1/transfers/quote", booked(b.HandleQuote))
	mux.HandleFunc("GET /v1/nip/sends/open", s.openSends)
	mux.HandleFunc("POST /v1/nip/sends/{session_id}/status-query", booked(b.HandleStatusQuery))
	mux.HandleFunc("POST /v1/deposits", write(b.HandleCash("deposit")))
	mux.HandleFunc("POST /v1/withdrawals", write(b.HandleCash("withdrawal")))
	mux.HandleFunc("GET /v1/closure-payouts", booked(b.HandleClosurePayouts))
	mux.HandleFunc("POST /v1/closure-payouts", booked(closing(write(b.HandleClosurePayout))))
	mux.HandleFunc("GET /v1/ledger/check", booked(b.HandleLedgerCheck))
	mux.HandleFunc("GET /v1/books/totals", booked(b.HandleTotals))
	mux.HandleFunc("GET /v1/books/accounts/{nuban}/entries", booked(b.HandleEntries))
	mux.HandleFunc("GET /v1/books/accounts/{nuban}/entries/{entry_id}", booked(b.HandleEntry))
	mux.HandleFunc("GET /v1/books/sessions/{session_id}", booked(b.HandleSessionEntries))
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		web.WriteError(w, r, http.StatusNotFound, "not_found", "A bank does not serve this control call yet.")
	})
	return web.WithRequestID(s.guard(mux))
}

func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.link.Allowed(r) {
			web.WriteError(w, r, http.StatusForbidden, "forbidden", "The control API only accepts local connections.")
			return
		}
		if r.Header.Get("Origin") != "" {
			web.WriteError(w, r, http.StatusForbidden, "forbidden", "Browser requests are refused.")
			return
		}
		if !s.link.TokenOK(r) {
			web.WriteError(w, r, http.StatusUnauthorized, "unauthenticated", "A valid control token is required.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	web.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "participant_id": s.id, "world_id": s.WorldID()})
}

func (s *Server) state() control.State {
	out := control.State{ParticipantID: s.id, Kind: participantKind, Steps: []control.Step{}}
	s.lifeMu.Lock()
	out.Phase, out.Reason = s.phase, s.reason
	out.Steps = append(out.Steps, s.steps...)
	s.lifeMu.Unlock()
	s.mu.Lock()
	out.Problems = slices.Clone(s.problems)
	out.SettlementProblems = slices.Clone(s.settlementProblems)
	out.SettlementCheckedAt = s.settlementCheckedAt
	s.mu.Unlock()
	gen := s.phaseGen.Load()
	out.Generation, out.ObservedGeneration = gen, gen
	return out
}

func (s *Server) openSends(w http.ResponseWriter, r *http.Request) {
	b := s.books.Load()
	if b == nil {
		web.WriteError(w, r, http.StatusServiceUnavailable, "unavailable", booksNotReady)
		return
	}
	n, err := b.OpenSends(r.Context())
	if err != nil {
		s.log.Error("count open sends", "err", err)
		web.WriteError(w, r, http.StatusServiceUnavailable, "unavailable", "The bank could not count its open transfers. Try again soon.")
		return
	}
	web.WriteJSON(w, http.StatusOK, control.OpenSends{Open: n})
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	web.WriteJSON(w, http.StatusOK, s.state())
}

func (s *Server) reload() error {
	f, err := config.LoadBankFile(s.dir, s.id)
	if err != nil {
		return err
	}
	want := f.Bank()
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	s.mu.Lock()
	changed := s.bank.Name != want.Name || s.bank.API != want.API
	s.bank.Name, s.bank.State, s.bank.NetDebitCap, s.bank.Collateral, s.bank.API = want.Name, want.State, want.NetDebitCap, want.Collateral, want.API
	s.mu.Unlock()
	if changed {
		s.stale.Store(true)
	}
	return nil
}

func (s *Server) setPhase(w http.ResponseWriter, r *http.Request) {
	var req control.PhaseRequest
	if err := web.DecodeBody(r, &req); err != nil || !validPhase(req.Phase) || req.Generation < 0 || req.WorldID == "" {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", `Send {"phase":"active","generation":1,"world_id":"wld_..."} with a phase, a generation and a world ID.`)
		return
	}
	if err := s.reload(); err != nil {
		s.log.Error("bank config not reloaded", "bank", s.id, "err", err)
		web.WriteError(w, r, http.StatusInternalServerError, "config_unreadable", "The bank config could not be read. Fix banks/"+s.id+"/"+config.BankFileName+" and send the phase again.")
		return
	}
	s.lifeMu.Lock()
	s.phase, s.reason = req.Phase, req.Reason
	s.lifeMu.Unlock()
	s.mu.Lock()
	s.worldID = req.WorldID
	s.mu.Unlock()
	s.phaseGen.Store(req.Generation)
	web.WriteJSON(w, http.StatusOK, s.state())
}

func faultPage(r *http.Request) (limit int, cursor string, ok bool) {
	limit = faultPageDefault
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > control.FaultPageMax {
			return 0, "", false
		}
		limit = n
	}
	return limit, r.URL.Query().Get("cursor"), true
}

func (s *Server) listFaults(w http.ResponseWriter, r *http.Request) {
	limit, cursor, ok := faultPage(r)
	if !ok {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", fmt.Sprintf("limit must be a number from 1 to %d.", control.FaultPageMax))
		return
	}
	live := s.faults.Live(s.clock.Now())
	start := 0
	if cursor != "" {
		i := slices.IndexFunc(live, func(f control.Fault) bool { return f.ID == cursor })
		if i < 0 {
			web.WriteError(w, r, http.StatusBadRequest, "invalid_cursor", "That cursor is not valid. Use the next_cursor value from the last page.")
			return
		}
		start = i + 1
	}
	end := min(start+limit, len(live))
	data := make([]control.FaultView, 0, end-start)
	for _, f := range live[start:end] {
		data = append(data, viewFault(f))
	}
	page := map[string]any{"data": data}
	if end < len(live) {
		page["next_cursor"] = live[end-1].ID
	}
	web.WriteJSON(w, http.StatusOK, page)
}

func (s *Server) setFault(w http.ResponseWriter, r *http.Request) {
	var req control.FaultRequest
	if err := web.DecodeBody(r, &req); err != nil || req.Bank != "" {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", `Send {"kind":"down","for":"1h"} or {"kind":"timeout","outcome":"credited"}.`)
		return
	}
	now := s.clock.Now()
	f, err := control.NewFault(req, now)
	if err != nil {
		web.WriteError(w, r, http.StatusUnprocessableEntity, "invalid_request", strings.TrimPrefix(err.Error(), control.ErrInvalid.Error()+": "))
		return
	}
	f.ID, f.Bank = control.FaultID(r.Header.Get("Idempotency-Key")), s.id
	s.faults.Add(f, now)
	web.WriteJSON(w, http.StatusCreated, viewFault(f))
}

func (s *Server) clearFaults(w http.ResponseWriter, r *http.Request) {
	prefix := strings.Join([]string{web.ControlOwner, http.MethodPost, "/v1/faults", ""}, " ")
	cleared := s.faults.Clear(s.clock.Now())
	s.mu.Lock()
	for scope := range s.replays {
		if strings.HasPrefix(scope, prefix) {
			delete(s.replays, scope)
		}
	}
	s.mu.Unlock()
	web.WriteJSON(w, http.StatusOK, map[string]int{"cleared": cleared})
}
