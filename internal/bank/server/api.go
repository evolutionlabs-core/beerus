package server

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

const (
	participantKind  = "bank"
	faultPageDefault = 50
	FaultPageMax     = 500
)

func validPhase(p string) bool {
	switch p {
	case config.PhaseProvisioning, config.PhaseActive, config.PhaseSuspended, config.PhaseRepairing, config.PhaseClosing, config.PhaseClosed, config.PhaseTerminating:
		return true
	}
	return false
}

type State struct {
	ParticipantID      string `json:"participant_id"`
	Kind               string `json:"kind"`
	Phase              string `json:"phase"`
	Reason             string `json:"reason,omitempty"`
	Generation         int64  `json:"generation"`
	ObservedGeneration int64  `json:"observed_generation"`
	Steps              []Step `json:"steps"`
}

type PhaseRequest struct {
	Phase      string `json:"phase"`
	Reason     string `json:"reason"`
	Generation int64  `json:"generation"`
	WorldID    string `json:"world_id"`
}

type FaultView struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	Outcome   string     `json:"outcome,omitempty"`
	Left      int        `json:"left,omitempty"`
	UntilAt   *time.Time `json:"until_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
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

func viewFault(f Fault) FaultView {
	v := FaultView{ID: f.ID, Kind: f.Kind, Outcome: f.Outcome, Left: f.Left, CreatedAt: f.At.In(clock.Lagos)}
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
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.health)
	mux.HandleFunc("GET /v1/status", s.status)
	mux.HandleFunc("PUT /v1/phase", s.setPhase)
	mux.HandleFunc("GET /v1/faults", s.listFaults)
	mux.HandleFunc("POST /v1/faults", idempotent(s.setFault))
	mux.HandleFunc("DELETE /v1/faults", s.clearFaults)
	mux.HandleFunc("GET /v1/customers", s.banking.HandleCustomers)
	mux.HandleFunc("POST /v1/customers", idempotent(s.banking.HandleCreateCustomer))
	mux.HandleFunc("POST /v1/customers/{customer_id}/close", idempotent(s.banking.HandleCloseCustomer))
	mux.HandleFunc("GET /v1/accounts", s.banking.HandleAccounts)
	mux.HandleFunc("POST /v1/accounts", idempotent(s.banking.HandleOpenAccount))
	mux.HandleFunc("POST /v1/accounts/{nuban}/close", idempotent(s.banking.HandleCloseAccount))
	mux.HandleFunc("GET /v1/accounts/{nuban}/statement", s.banking.HandleStatement)
	mux.HandleFunc("POST /v1/transfers", idempotent(s.banking.HandleTransfer))
	mux.HandleFunc("POST /v1/transfers/quote", s.banking.HandleQuote)
	mux.HandleFunc("GET /v1/nip/sends/open", s.openSends)
	mux.HandleFunc("POST /v1/nip/sends/{session_id}/status-query", s.banking.HandleStatusQuery)
	mux.HandleFunc("POST /v1/deposits", idempotent(s.banking.HandleCash("deposit")))
	mux.HandleFunc("POST /v1/withdrawals", idempotent(s.banking.HandleCash("withdrawal")))
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

func (s *Server) state() State {
	out := State{ParticipantID: s.id, Kind: participantKind, Steps: []Step{}}
	s.lifeMu.Lock()
	out.Phase, out.Reason = s.phase, s.reason
	out.Steps = append(out.Steps, s.steps...)
	s.lifeMu.Unlock()
	gen := s.phaseGen.Load()
	out.Generation, out.ObservedGeneration = gen, gen
	return out
}

type OpenSends struct {
	Open int `json:"open_sends"`
}

func (s *Server) openSends(w http.ResponseWriter, r *http.Request) {
	n, err := s.books.OpenSends(r.Context())
	if err != nil {
		s.log.Error("count open sends", "err", err)
		web.WriteError(w, r, http.StatusServiceUnavailable, "unavailable", "The bank could not count its open transfers. Try again soon.")
		return
	}
	web.WriteJSON(w, http.StatusOK, OpenSends{Open: n})
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
	var req PhaseRequest
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
		if err != nil || n < 1 || n > FaultPageMax {
			return 0, "", false
		}
		limit = n
	}
	return limit, r.URL.Query().Get("cursor"), true
}

func (s *Server) listFaults(w http.ResponseWriter, r *http.Request) {
	limit, cursor, ok := faultPage(r)
	if !ok {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", fmt.Sprintf("limit must be a number from 1 to %d.", FaultPageMax))
		return
	}
	live := s.faults.Live(s.clock.Now())
	start := 0
	if cursor != "" {
		i := slices.IndexFunc(live, func(f Fault) bool { return f.ID == cursor })
		if i < 0 {
			web.WriteError(w, r, http.StatusBadRequest, "invalid_cursor", "That cursor is not valid. Use the next_cursor value from the last page.")
			return
		}
		start = i + 1
	}
	end := min(start+limit, len(live))
	data := make([]FaultView, 0, end-start)
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
	var req FaultRequest
	if err := web.DecodeBody(r, &req); err != nil || req.Bank != "" {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", `Send {"kind":"down","for":"1h"} or {"kind":"timeout","outcome":"credited"}.`)
		return
	}
	now := s.clock.Now()
	f, err := NewFault(req, now)
	if err != nil {
		web.WriteError(w, r, http.StatusUnprocessableEntity, "invalid_request", strings.TrimPrefix(err.Error(), ErrInvalid.Error()+": "))
		return
	}
	f.ID, f.Bank = FaultID(r.Header.Get("Idempotency-Key")), s.id
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
