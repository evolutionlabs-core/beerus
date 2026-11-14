package inbound

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/nip"
)

const (
	maxBodyBytes   = 64 << 10
	sessionDigits  = 30
	nubanDigits    = 10
	nipCodeDigits  = 6
	narrationLimit = 100
	requestIDChars = 12
)

type Caller struct {
	CommonName string
	Role       string
}

type server struct {
	bank   *bank.Bank
	now    func() time.Time
	caller Caller
	log    *slog.Logger
}

type apiError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

type accountName struct {
	ResponseCode  string `json:"response_code"`
	AccountNumber string `json:"account_number"`
	AccountName   string `json:"account_name,omitempty"`
}

type creditResult struct {
	SessionID    string `json:"session_id"`
	ResponseCode string `json:"response_code"`
}

func New(b *bank.Bank, now func() time.Time, caller Caller, log *slog.Logger) http.Handler {
	s := &server{bank: b, now: now, caller: caller, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/nip/account-names/{account_number}", only(http.MethodGet, s.nameEnquiry))
	mux.HandleFunc("/v1/nip/credits", only(http.MethodPost, s.credit))
	mux.HandleFunc("/v1/nip/credits/{session_id}", only(http.MethodGet, s.creditStatus))
	mux.HandleFunc("/v1/nip/", notFound)
	return s.guard(mux)
}

func only(method string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", fmt.Sprintf("Use %s for this path.", method))
			return
		}
		next(w, r)
	}
}

func notFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "not_found", "This path is not part of the NIP API of a bank.")
}

func WriteUnavailable(w http.ResponseWriter) {
	writeError(w, http.StatusServiceUnavailable, "unavailable", "This bank is not available right now. Try again soon.")
}

func requestID() string {
	return "req_" + rand.Text()[:requestIDChars]
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		status = http.StatusInternalServerError
		b = []byte(`{"error":{"code":"internal_error","message":"The response could not be written.","request_id":"` + requestID() + `"}}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]apiError{"error": {Code: code, Message: message, RequestID: requestID()}})
}

func (s *server) unavailable(w http.ResponseWriter, r *http.Request, err error) {
	s.log.ErrorContext(r.Context(), "nip request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	WriteUnavailable(w)
}

func (s *server) authorized(r *http.Request) bool {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		return false
	}
	leaf := r.TLS.PeerCertificates[0]
	return leaf.Subject.CommonName == s.caller.CommonName && slices.Contains(leaf.Subject.OrganizationalUnit, s.caller.Role)
}

func (s *server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			writeError(w, http.StatusForbidden, "forbidden", "This certificate may not call the NIP API of this bank. Only NIBSS may.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func allDigits(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func (s *server) nameEnquiry(w http.ResponseWriter, r *http.Request) {
	nuban := r.PathValue("account_number")
	if !allDigits(nuban, nubanDigits) {
		writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("account_number must have %d digits.", nubanDigits))
		return
	}
	name, err := s.bank.NameEnquiry(r.Context(), nuban)
	switch {
	case errors.Is(err, nip.ErrInvalidAccount):
		writeJSON(w, http.StatusOK, accountName{ResponseCode: nip.InvalidAccount, AccountNumber: nuban})
	case errors.Is(err, nip.ErrNoRecord):
		writeJSON(w, http.StatusOK, accountName{ResponseCode: nip.NoRecord, AccountNumber: nuban})
	case err != nil:
		s.unavailable(w, r, err)
	default:
		writeJSON(w, http.StatusOK, accountName{ResponseCode: nip.OK, AccountNumber: nuban, AccountName: name})
	}
}

type tooLargeError struct {
	limit int
}

func (e *tooLargeError) Error() string {
	return fmt.Sprintf("body is larger than %d bytes", e.limit)
}

func readBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(b) > maxBodyBytes {
		return nil, &tooLargeError{limit: maxBodyBytes}
	}
	return b, nil
}

func decodeCredit(body []byte) (bank.NIPCredit, error) {
	var c bank.NIPCredit
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return bank.NIPCredit{}, fmt.Errorf("decode credit: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return bank.NIPCredit{}, errors.New("decode credit: data after the body")
	}
	return c, nil
}

func validate(c bank.NIPCredit) string {
	switch {
	case !allDigits(c.SessionID, sessionDigits):
		return fmt.Sprintf("session_id must have %d digits.", sessionDigits)
	case !allDigits(c.FromCode, nipCodeDigits):
		return fmt.Sprintf("from_code must have %d digits.", nipCodeDigits)
	case c.FromBankName == "":
		return "from_bank_name is required."
	case !allDigits(c.FromNUBAN, nubanDigits):
		return fmt.Sprintf("from_nuban must have %d digits.", nubanDigits)
	case c.FromName == "":
		return "from_name is required."
	case !allDigits(c.ToNUBAN, nubanDigits):
		return fmt.Sprintf("to_nuban must have %d digits.", nubanDigits)
	case c.Amount < 1:
		return "amount_minor must be above zero."
	case utf8.RuneCountInString(c.Narration) > narrationLimit:
		return fmt.Sprintf("narration must have at most %d characters.", narrationLimit)
	case c.CreatedAt.IsZero():
		return "created_at is required and must be an RFC 3339 time."
	}
	return ""
}

func reusedMessage(e *bank.SessionReusedError) string {
	if len(e.Fields) == 0 {
		return fmt.Sprintf("Session %s was already credited with different details. Repeat the first request.", e.SessionID)
	}
	first := make([]string, len(e.Fields))
	now := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		first[i] = f.Field + " " + f.First
		now[i] = f.Now
	}
	return fmt.Sprintf("Session %s was first credited with %s, and this request has %s. Repeat the first request.", e.SessionID, strings.Join(first, " and "), strings.Join(now, " and "))
}

func (s *server) credit(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	var large *tooLargeError
	if errors.As(err, &large) {
		writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("The body is larger than %d bytes. Send a smaller body.", large.limit))
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "The body could not be read. Send it again.")
		return
	}
	c, err := decodeCredit(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Send a JSON credit request with only the documented fields as the body.")
		return
	}
	if msg := validate(c); msg != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	ans, err := s.bank.Credit(r.Context(), c, s.now())
	var reused *bank.SessionReusedError
	switch {
	case errors.As(err, &reused):
		writeError(w, http.StatusUnprocessableEntity, "idempotency_key_reused", reusedMessage(reused))
	case err != nil:
		s.unavailable(w, r, err)
	default:
		if ans.Replayed {
			w.Header().Set("Idempotent-Replayed", "true")
		}
		writeJSON(w, http.StatusOK, creditResult{SessionID: c.SessionID, ResponseCode: ans.Code})
	}
}

func (s *server) creditStatus(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("session_id")
	if !allDigits(sid, sessionDigits) {
		writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("session_id must have %d digits.", sessionDigits))
		return
	}
	code, err := s.bank.CreditStatus(r.Context(), sid, s.now())
	if err != nil {
		s.unavailable(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, creditResult{SessionID: sid, ResponseCode: code})
}
