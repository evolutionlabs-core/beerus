package participant

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/cbn"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

const (
	flightTTL      = time.Minute
	replayedHeader = "Idempotent-Replayed"
)

type sessionRequest struct {
	SessionID string    `json:"session_id"`
	CutoffAt  time.Time `json:"cutoff_at"`
	Nets      []cbn.Net `json:"nets"`
}

type balanceReply struct {
	BankID  string    `json:"bank_id"`
	Balance int64     `json:"balance_minor"`
	ReadAt  time.Time `json:"read_at"`
}

func (p *Participant) settlement() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/settlement-sessions", only(http.MethodPost, p.postSession))
	mux.HandleFunc("/v1/banks/{bank_id}/settlement-balance", only(http.MethodGet, p.balance))
	mux.HandleFunc("/v1/settlement-postings", only(http.MethodGet, p.postings))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		web.WriteError(w, r, http.StatusNotFound, "not_found", "The CBN does not serve this call.")
	})
	return web.WithRequestID(mux)
}

func callerIdentity(r *http.Request) (string, []string, bool) {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		return "", nil, false
	}
	leaf := r.TLS.PeerCertificates[0]
	return leaf.Subject.CommonName, leaf.Subject.OrganizationalUnit, true
}

func isNIBSS(r *http.Request) bool {
	identity, roles, ok := callerIdentity(r)
	return ok && identity == config.NIBSSCN && slices.Contains(roles, config.NIBSSRole)
}

func bankCaller(r *http.Request) (string, bool) {
	identity, roles, ok := callerIdentity(r)
	return identity, ok && slices.Contains(roles, config.BankRole)
}

func (p *Participant) postSession(w http.ResponseWriter, r *http.Request) {
	if !isNIBSS(r) {
		web.WriteError(w, r, http.StatusForbidden, "forbidden", "This certificate may not post a settlement session. Only NIBSS may.")
		return
	}
	var req sessionRequest
	if err := web.DecodeBody(r, &req); err != nil || req.SessionID == "" || req.CutoffAt.IsZero() || len(req.Nets) < 2 {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "Send a session_id, a cutoff_at and at least 2 nets.")
		return
	}
	if !p.begin(req.SessionID) {
		web.WriteError(w, r, http.StatusConflict, "session_in_flight", fmt.Sprintf("Session %s is still being posted. Send it again in a moment.", req.SessionID))
		return
	}
	defer p.end(req.SessionID)
	posting, replayed, err := p.books.Post(r.Context(), req.SessionID, req.CutoffAt, req.Nets)
	if err != nil {
		p.refused(w, r, "session not posted", err)
		return
	}
	if replayed {
		w.Header().Set(replayedHeader, "true")
	}
	web.WriteJSON(w, http.StatusOK, posting)
}

func (p *Participant) begin(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.clock.Real()
	for k, at := range p.flight {
		if now.Sub(at) > flightTTL {
			delete(p.flight, k)
		}
	}
	if _, running := p.flight[id]; running {
		return false
	}
	p.flight[id] = now
	return true
}

func (p *Participant) end(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.flight, id)
}

func (p *Participant) refused(w http.ResponseWriter, r *http.Request, msg string, err error) {
	var bad *cbn.InvalidError
	var unbalanced *cbn.UnbalancedError
	var large *cbn.TooLargeError
	var dup *cbn.DuplicateError
	var unknown *cbn.UnknownBankError
	var reused *cbn.ReusedError
	switch {
	case errors.As(err, &bad):
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", bad.Msg)
	case errors.As(err, &unbalanced):
		web.WriteError(w, r, http.StatusUnprocessableEntity, "settlement_unbalanced", unbalanced.Error())
	case errors.As(err, &large):
		web.WriteError(w, r, http.StatusUnprocessableEntity, "amount_too_large", large.Error())
	case errors.As(err, &dup):
		web.WriteError(w, r, http.StatusUnprocessableEntity, "duplicate_bank", dup.Error())
	case errors.As(err, &unknown):
		web.WriteError(w, r, http.StatusUnprocessableEntity, "unknown_bank", unknown.Error())
	case errors.As(err, &reused):
		web.WriteError(w, r, http.StatusUnprocessableEntity, "session_reused", reused.Error())
	default:
		p.unavailable(w, r, msg, err)
	}
}

func (p *Participant) balance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("bank_id")
	if who, ok := bankCaller(r); !ok || who != id {
		web.WriteError(w, r, http.StatusForbidden, "forbidden", fmt.Sprintf("This certificate may not read the balance of %s.", id))
		return
	}
	bal, err := p.books.Balance(r.Context(), id)
	if errors.Is(err, cbn.ErrNoAccount) {
		web.WriteError(w, r, http.StatusNotFound, "not_found", "No settlement account for that bank.")
		return
	}
	if err != nil {
		p.unavailable(w, r, "balance not read", err)
		return
	}
	web.WriteJSON(w, http.StatusOK, balanceReply{BankID: id, Balance: bal, ReadAt: p.clock.Now()})
}

func (p *Participant) postings(w http.ResponseWriter, r *http.Request) {
	bankID := r.URL.Query().Get("bank_id")
	if who, ok := bankCaller(r); ok {
		if bankID != "" && bankID != who {
			web.WriteError(w, r, http.StatusForbidden, "forbidden", fmt.Sprintf("This certificate may not read the postings of %s.", bankID))
			return
		}
		bankID = who
	} else if !isNIBSS(r) {
		web.WriteError(w, r, http.StatusForbidden, "forbidden", "This certificate may not read settlement postings.")
		return
	}
	limit := cbn.DefaultPage
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > cbn.MaxPage {
			web.WriteError(w, r, http.StatusBadRequest, "invalid_request", fmt.Sprintf("The limit must be a number from 1 to %d.", cbn.MaxPage))
			return
		}
		limit = n
	}
	list, next, err := p.books.Postings(r.Context(), bankID, limit, r.URL.Query().Get("cursor"))
	if errors.Is(err, cbn.ErrCursor) {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_cursor", "That cursor is not valid. Use the next_cursor value from the last page.")
		return
	}
	if err != nil {
		p.unavailable(w, r, "postings not listed", err)
		return
	}
	web.WriteJSON(w, http.StatusOK, web.ListPage(list, next))
}
