package participant

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/evolutionlabs-core/beerus/internal/nibss"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

const narrationLimit = 100

type transferRequest struct {
	SessionID string `json:"session_id"`
	FromCode  string `json:"from_code"`
	ToCode    string `json:"to_code"`
	FromNUBAN string `json:"from_nuban"`
	ToNUBAN   string `json:"to_nuban"`
	FromName  string `json:"from_name"`
	Amount    int64  `json:"amount_minor"`
	Narration string `json:"narration"`
	Closure   bool   `json:"closure"`
}

func (t transferRequest) problem() string {
	switch {
	case !nip.ValidSessionID(t.SessionID):
		return "session_id must have 30 digits."
	case !nipCodeRe.MatchString(t.FromCode) || !strings.HasPrefix(t.SessionID, t.FromCode):
		return fmt.Sprintf("from_code must have %d digits and start the session_id.", nipCodeDigits)
	case !nipCodeRe.MatchString(t.ToCode):
		return fmt.Sprintf("to_code must have %d digits.", nipCodeDigits)
	case !nubanRe.MatchString(t.FromNUBAN):
		return fmt.Sprintf("from_nuban must have %d digits.", nubanDigits)
	case !nubanRe.MatchString(t.ToNUBAN):
		return fmt.Sprintf("to_nuban must have %d digits.", nubanDigits)
	case strings.TrimSpace(t.FromName) == "":
		return "from_name is required."
	case t.Amount < 1:
		return "amount_minor must be above zero."
	case utf8.RuneCountInString(t.Narration) > narrationLimit:
		return fmt.Sprintf("narration must have at most %d characters.", narrationLimit)
	case strings.IndexFunc(t.Narration, unicode.IsControl) >= 0:
		return "narration must not contain control characters."
	}
	return ""
}

func (p *Participant) transfer(w http.ResponseWriter, r *http.Request) {
	identity, ok := p.identify(w, r, "send transfers")
	if !ok {
		return
	}
	var req transferRequest
	if err := web.DecodeBody(r, &req); err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "Send a JSON funds transfer with only the documented fields as the body.")
		return
	}
	if msg := req.problem(); msg != "" {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	if !p.owns(r.Context(), identity, req.FromCode) {
		web.WriteError(w, r, http.StatusForbidden, "forbidden", fmt.Sprintf("This certificate may not send transfers for bank %s.", req.FromCode))
		return
	}
	now := p.clock.Now()
	begun := nibss.Session{ID: req.SessionID, FromCode: req.FromCode, ToCode: req.ToCode, FromNUBAN: req.FromNUBAN, ToNUBAN: req.ToNUBAN, Amount: req.Amount, Narration: req.Narration, CreatedAt: now, Closure: req.Closure}
	sess, replayed, err := p.store.Begin(r.Context(), begun)
	if p.beginFailed(w, r, req, err) {
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
		web.WriteJSON(w, http.StatusOK, sess)
		return
	}
	if sess.Status == nibss.SessionPending {
		sess, err = p.credit(context.WithoutCancel(r.Context()), req, sess)
		if err != nil {
			p.unavailable(w, r, "transfer not completed", err)
			return
		}
	}
	web.WriteJSON(w, http.StatusOK, sess)
}

func (p *Participant) owns(ctx context.Context, identity, code string) bool {
	m, err := p.store.MemberAt(ctx, code)
	if err != nil && !errors.Is(err, nip.ErrUnknownInstitution) {
		p.log.ErrorContext(ctx, "certificate identity not checked", "err", err)
	}
	return err == nil && m.Status != nip.StatusRemoved && m.CertificateIdentity == identity
}

func (p *Participant) beginFailed(w http.ResponseWriter, r *http.Request, req transferRequest, err error) bool {
	var reused *nibss.ReusedError
	var closure *nibss.ClosureError
	switch {
	case err == nil:
		return false
	case errors.As(err, &reused):
		web.WriteError(w, r, http.StatusUnprocessableEntity, "idempotency_key_reused", reusedMessage(reused))
	case errors.As(err, &closure):
		web.WriteError(w, r, http.StatusUnprocessableEntity, "closure_not_allowed", fmt.Sprintf("Bank %s is %s, so it cannot send a closure transfer. Close the bank first, and send it again when the bank is terminating.", req.FromCode, closure.Status))
	case errors.Is(err, nip.ErrUnknownInstitution):
		web.WriteError(w, r, http.StatusUnprocessableEntity, "unknown_bank", fmt.Sprintf("Bank code %s is not in the NIBSS directory.", req.ToCode))
	default:
		p.unavailable(w, r, "transfer not recorded", err)
	}
	return true
}

func reusedMessage(e *nibss.ReusedError) string {
	first := make([]string, len(e.Fields))
	now := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		first[i] = f.Field + " " + f.First
		now[i] = f.Now
	}
	return fmt.Sprintf("Session %s was first sent with %s, and this request has %s. Send a new session_id, or repeat the first request.", e.ID, strings.Join(first, " and "), strings.Join(now, " and "))
}

func (p *Participant) credit(ctx context.Context, req transferRequest, sess nibss.Session) (nibss.Session, error) {
	from, err := p.store.MemberAt(ctx, req.FromCode)
	if err != nil {
		return nibss.Session{}, err
	}
	to, err := p.store.MemberAt(ctx, req.ToCode)
	if err != nil {
		return nibss.Session{}, err
	}
	code, err := p.banks.credit(ctx, to, creditBody{SessionID: req.SessionID, FromCode: req.FromCode, FromBankName: from.Name, FromNUBAN: req.FromNUBAN, FromName: req.FromName, ToNUBAN: req.ToNUBAN, Amount: req.Amount, Narration: req.Narration, CreatedAt: sess.CreatedAt})
	if err != nil {
		p.log.WarnContext(ctx, "bank gave no answer to a credit", "bank", to.ID, "session", req.SessionID, "err", err)
		return p.store.NoAnswer(ctx, req.SessionID, p.clock.Now().Add(p.file.StatusQueryAfter()))
	}
	return p.settle(ctx, req.SessionID, code)
}

func (p *Participant) settle(ctx context.Context, id, code string) (nibss.Session, error) {
	outcome := nip.OutcomeFailed
	if code == nibss.SessionOK {
		outcome = nip.OutcomeCredited
	}
	if err := p.store.Resolve(ctx, id, code, outcome, p.clock.Now()); err != nil {
		return nibss.Session{}, err
	}
	return p.store.Session(ctx, id)
}

func (p *Participant) statusQuery(w http.ResponseWriter, r *http.Request) {
	identity, ok := p.identify(w, r, "ask about transfers")
	if !ok {
		return
	}
	id := r.PathValue("session_id")
	if !nip.ValidSessionID(id) {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "session_id must have 30 digits.")
		return
	}
	sess, err := p.store.Session(r.Context(), id)
	if errors.Is(err, nip.ErrNoRecord) {
		web.WriteError(w, r, http.StatusNotFound, "not_found", fmt.Sprintf("NIBSS has no session %s. Send the transfer again with the same session_id.", id))
		return
	}
	if err != nil {
		p.unavailable(w, r, "session not read", err)
		return
	}
	if !p.owns(r.Context(), identity, sess.FromCode) {
		web.WriteError(w, r, http.StatusForbidden, "forbidden", fmt.Sprintf("This certificate may not ask about transfers of bank %s.", sess.FromCode))
		return
	}
	sess, err = p.query(context.WithoutCancel(r.Context()), sess)
	if err != nil {
		p.unavailable(w, r, "status query not answered", err)
		return
	}
	web.WriteJSON(w, http.StatusOK, sess)
}

func (p *Participant) query(ctx context.Context, sess nibss.Session) (nibss.Session, error) {
	now := p.clock.Now()
	sess, err := p.store.TSQ(ctx, sess.ID, now.Add(p.file.StatusQueryAfter()))
	if err != nil || sess.Status != nibss.SessionPending {
		return sess, err
	}
	to, err := p.store.MemberAt(ctx, sess.ToCode)
	if err != nil {
		return nibss.Session{}, err
	}
	code, err := p.banks.creditStatus(ctx, to, sess.ID)
	if err == nil {
		return p.settle(ctx, sess.ID, code)
	}
	p.log.WarnContext(ctx, "bank gave no answer to a status query", "bank", to.ID, "session", sess.ID, "attempts", sess.TSQAttempts, "err", err)
	if sess.TSQAttempts < p.file.TSQAttempts {
		return sess, nil
	}
	return p.settle(ctx, sess.ID, nip.Unavailable)
}
