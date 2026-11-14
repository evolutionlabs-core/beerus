package connect

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

const (
	participantKind   = "bank"
	faultPageDefault  = 50
	faultPageMax      = 500
	idempotencyKeyMax = 255
	faultIDHexChars   = 20
	waitingForPhase   = "Waiting for the control plane to send the phase."
)

var participantPhases = []string{PhaseProvisioning, PhaseActive, PhaseSuspended, PhaseRepairing, PhaseClosing, PhaseClosed, PhaseTerminating}

type participantStep struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Done        bool       `json:"done"`
	Error       string     `json:"error,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type participantState struct {
	ParticipantID      string            `json:"participant_id"`
	Kind               string            `json:"kind"`
	Phase              string            `json:"phase"`
	Reason             string            `json:"reason,omitempty"`
	Generation         int64             `json:"generation"`
	ObservedGeneration int64             `json:"observed_generation"`
	Steps              []participantStep `json:"steps"`
}

type phaseRequest struct {
	Phase      string `json:"phase"`
	Reason     string `json:"reason"`
	Generation int64  `json:"generation"`
	WorldID    string `json:"world_id"`
}

type participantFault struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	Outcome   string     `json:"outcome,omitempty"`
	Left      int        `json:"left,omitempty"`
	UntilAt   *time.Time `json:"until_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

func faultView(f Fault) participantFault {
	v := participantFault{ID: f.ID, Kind: f.Kind, Outcome: f.Outcome, Left: f.Left, CreatedAt: f.At.In(clock.Lagos)}
	if f.Until != nil {
		until := f.Until.In(clock.Lagos)
		v.UntilAt = &until
	}
	return v
}

func faultIDFor(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "flt_" + hex.EncodeToString(sum[:])[:faultIDHexChars]
}

func (a *App) participantAPI() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", a.participantHealth)
	mux.HandleFunc("GET /v1/status", a.participantStatus)
	mux.HandleFunc("PUT /v1/phase", a.setParticipantPhase)
	mux.HandleFunc("GET /v1/faults", a.listParticipantFaults)
	mux.HandleFunc("POST /v1/faults", a.idempotent(a.setParticipantFault))
	mux.HandleFunc("DELETE /v1/faults", a.clearParticipantFaults)
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusNotFound, "not_found", "A bank does not serve this control call yet.")
	})
	return a.withRequestID(a.participantGuard(mux))
}

func (a *App) participantGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			writeError(w, r, http.StatusForbidden, "forbidden", "Browser requests are refused.")
			return
		}
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(tok), []byte(a.ctlToken)) != 1 {
			writeError(w, r, http.StatusUnauthorized, "unauthenticated", "A valid control token is required.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) participantHealth(w http.ResponseWriter, r *http.Request) {
	if !a.phased.Load() {
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", "The participant is still starting. Try again soon.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "participant_id": a.bankID, "world_id": a.WorldID()})
}

func (a *App) participantState() participantState {
	out := participantState{ParticipantID: a.bankID, Kind: participantKind, Steps: []participantStep{}}
	a.lifeMu.Lock()
	if st := a.status[a.bankID]; st != nil {
		out.Phase, out.Reason = st.Phase, st.Reason
		for _, s := range st.Steps {
			out.Steps = append(out.Steps, participantStep{ID: s.ID, Name: s.Name, Done: s.Done, Error: s.Error, StartedAt: s.StartedAt, CompletedAt: s.At})
		}
	}
	a.lifeMu.Unlock()
	if !a.phased.Load() {
		out.Phase, out.Reason = PhaseProvisioning, waitingForPhase
		return out
	}
	gen := a.phaseGen.Load()
	out.Generation, out.ObservedGeneration = gen, gen
	return out
}

func (a *App) participantStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.participantState())
}

func (a *App) setParticipantPhase(w http.ResponseWriter, r *http.Request) {
	var req phaseRequest
	if err := decodeBody(r, &req); err != nil || !slices.Contains(participantPhases, req.Phase) || req.Generation < 0 || req.WorldID == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", `Send {"phase":"active","generation":1,"world_id":"wld_..."} with a phase, a generation and a world ID.`)
		return
	}
	a.lifeMu.Lock()
	if st := a.status[a.bankID]; st != nil {
		st.Phase, st.Reason = req.Phase, req.Reason
	}
	a.lifeMu.Unlock()
	a.mu.Lock()
	a.st.WorldID = req.WorldID
	a.mu.Unlock()
	a.phaseGen.Store(req.Generation)
	a.phased.Store(true)
	writeJSON(w, http.StatusOK, a.participantState())
}

func faultPage(r *http.Request) (limit int, cursor string, ok bool) {
	limit = faultPageDefault
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > faultPageMax {
			return 0, "", false
		}
		limit = n
	}
	return limit, r.URL.Query().Get("cursor"), true
}

func (a *App) listParticipantFaults(w http.ResponseWriter, r *http.Request) {
	limit, cursor, ok := faultPage(r)
	if !ok {
		writeError(w, r, http.StatusBadRequest, "invalid_request", fmt.Sprintf("limit must be a number from 1 to %d.", faultPageMax))
		return
	}
	live := a.Faults()
	start := 0
	if cursor != "" {
		i := slices.IndexFunc(live, func(f Fault) bool { return f.ID == cursor })
		if i < 0 {
			writeError(w, r, http.StatusBadRequest, "invalid_cursor", "That cursor is not valid. Use the next_cursor value from the last page.")
			return
		}
		start = i + 1
	}
	end := min(start+limit, len(live))
	data := make([]participantFault, 0, end-start)
	for _, f := range live[start:end] {
		data = append(data, faultView(f))
	}
	page := map[string]any{"data": data}
	if end < len(live) {
		page["next_cursor"] = live[end-1].ID
	}
	writeJSON(w, http.StatusOK, page)
}

func (a *App) setParticipantFault(w http.ResponseWriter, r *http.Request) {
	var req FaultRequest
	if err := decodeBody(r, &req); err != nil || req.Bank != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", `Send {"kind":"down","for":"1h"} or {"kind":"timeout","outcome":"credited"}.`)
		return
	}
	now := a.clock.Now()
	f, err := newFault(req, now)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "invalid_request", strings.TrimPrefix(err.Error(), ErrBankInvalid.Error()+": "))
		return
	}
	f.ID, f.Bank = faultIDFor(r.Header.Get("Idempotency-Key")), a.bankID
	a.mu.Lock()
	a.pruneFaultsLocked(now)
	a.st.Faults = append(a.st.Faults, &f)
	a.dirty()
	a.mu.Unlock()
	writeJSON(w, http.StatusCreated, faultView(f))
}

func (a *App) clearParticipantFaults(w http.ResponseWriter, r *http.Request) {
	cleared := a.ClearFaults("")
	prefix := strings.Join([]string{controlOwner, http.MethodPost, "/v1/faults", ""}, " ")
	a.mu.Lock()
	for scope := range a.st.Idem {
		if strings.HasPrefix(scope, prefix) {
			delete(a.st.Idem, scope)
		}
	}
	a.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]int{"cleared": cleared})
}

func (a *App) bankRefusing(id string) bool {
	if a.role == RoleBank && !a.phased.Load() {
		return true
	}
	return a.bankPhase(id) == PhaseSuspended || a.bankDown(id, a.clock.Now())
}
