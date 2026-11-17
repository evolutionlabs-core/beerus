package server

import (
	"errors"
	"net/http"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/nip"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

const maxKey = 255

func (s *Banking) sender(w http.ResponseWriter, r *http.Request) (*Sender, bool) {
	bk, err := s.bankBook(s.bankIDOf(r))
	if err != nil {
		s.Error(w, r, err)
		return nil, false
	}
	if bk.Send == nil {
		s.Error(w, r, &UnavailableError{Msg: bk.Name + " cannot send transfers right now. Try again soon.", Err: errors.New("no sender")})
		return nil, false
	}
	return bk.Send, true
}

func (s *Banking) transferRequest(w http.ResponseWriter, r *http.Request) (TransferRequest, bool) {
	var body TransferBody
	if err := web.DecodeBody(r, &body); err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", `Send {"from":"9010000014","to_bank":"bank-b","to":"9020000011","amount":"25000"}.`)
		return TransferRequest{}, false
	}
	amount, err := bank.ParseNaira(body.Amount)
	if err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return TransferRequest{}, false
	}
	if body.From == "" || body.To == "" {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "from and to are required account numbers.")
		return TransferRequest{}, false
	}
	key := r.Header.Get("Idempotency-Key")
	if len(key) > maxKey {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "An Idempotency-Key must be 1 to 255 characters.")
		return TransferRequest{}, false
	}
	return TransferRequest{FromBank: s.bankIDOf(r), From: body.From, ToBank: body.ToBank, To: body.To, Amount: amount, Narration: body.Narration, Key: key, Closure: body.Closure}, true
}

func (s *Banking) transferReply(w http.ResponseWriter, r *http.Request, res TransferResult, err error) {
	var te *TransferError
	if errors.As(err, &te) {
		web.WriteJSON(w, te.Status, map[string]any{"error": web.APIError{Code: te.ErrCode, Message: web.Sentence(te.Error()), RequestID: web.RequestID(r)}, "response_code": te.Code, "session_id": res.SessionID})
		return
	}
	if err != nil {
		s.Error(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, res)
}

func (s *Banking) HandleTransfer(w http.ResponseWriter, r *http.Request) {
	send, ok := s.sender(w, r)
	if !ok {
		return
	}
	req, ok := s.transferRequest(w, r)
	if !ok {
		return
	}
	res, err := send.Transfer(r.Context(), req)
	s.transferReply(w, r, res, err)
}

func (s *Banking) HandleQuote(w http.ResponseWriter, r *http.Request) {
	send, ok := s.sender(w, r)
	if !ok {
		return
	}
	req, ok := s.transferRequest(w, r)
	if !ok {
		return
	}
	res, err := send.Quote(r.Context(), req)
	s.transferReply(w, r, res, err)
}

func (s *Banking) HandleStatusQuery(w http.ResponseWriter, r *http.Request) {
	send, ok := s.sender(w, r)
	if !ok {
		return
	}
	sid := r.PathValue("session_id")
	if !nip.ValidSessionID(sid) {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", "session_id must have 30 digits.")
		return
	}
	sess, err := send.StatusQuery(r.Context(), sid)
	var peer *PeerError
	var away *UnavailableError
	switch {
	case errors.Is(err, ErrNoSend):
		web.WriteError(w, r, http.StatusNotFound, "not_found", "This bank has no send with that session ID.")
	case errors.Is(err, ErrSendRefused):
		web.WriteError(w, r, http.StatusUnprocessableEntity, "send_refused", "NIBSS refused the transfer, so the debit was reversed.")
	case errors.As(err, &away), errors.As(err, &peer):
		s.Log.Warn("status query not answered", "session", sid, "err", err)
		web.WriteError(w, r, http.StatusServiceUnavailable, "nibss_unavailable", "NIBSS could not be asked about the session. Try again soon.")
	case err != nil:
		s.Error(w, r, err)
	default:
		web.WriteJSON(w, http.StatusOK, sess)
	}
}
