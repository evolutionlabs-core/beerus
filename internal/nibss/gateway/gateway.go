package gateway

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/nip"
)

const (
	defaultLimit   = 50
	maxLimit       = 500
	requestIDChars = 12
)

type Lister func(ctx context.Context) ([]nip.Institution, error)

type server struct {
	list Lister
	log  *slog.Logger
}

type apiError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

type bankPage struct {
	Data       []nip.Institution `json:"data"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

func New(list Lister, log *slog.Logger) http.Handler {
	s := &server{list: list, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/banks", s.banks)
	mux.HandleFunc("/", notFound)
	return mux
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

func notFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "not_found", "This path is not part of the NIBSS API.")
}

func listed(all []nip.Institution, id string) bool {
	return slices.ContainsFunc(all, func(in nip.Institution) bool { return in.ID == id && in.Status != nip.StatusRemoved })
}

func (s *server) banks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use GET for this path.")
		return
	}
	id := r.Header.Get(config.CallerHeader)
	if id == "" {
		writeError(w, http.StatusUnauthorized, "unauthenticated", fmt.Sprintf("Send your ID in the %s header to read the bank directory.", config.CallerHeader))
		return
	}
	limit, ok := pageLimit(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("limit must be a number from 1 to %d.", maxLimit))
		return
	}
	all, err := s.list(r.Context())
	if err != nil {
		s.log.ErrorContext(r.Context(), "bank directory failed", "err", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "NIBSS is not available right now. Try again soon.")
		return
	}
	if !listed(all, id) {
		writeError(w, http.StatusForbidden, "forbidden", fmt.Sprintf("%s may not read the bank directory. It is not a bank NIBSS knows.", id))
		return
	}
	page, err := paginate(all, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_cursor", "That cursor is not valid. Use the next_cursor value from the last page.")
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func pageLimit(r *http.Request) (int, bool) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return defaultLimit, true
	}
	n, err := strconv.Atoi(v)
	return n, err == nil && n >= 1 && n <= maxLimit
}

func paginate(all []nip.Institution, cursor string, limit int) (bankPage, error) {
	kept := make([]nip.Institution, 0, len(all))
	for _, in := range all {
		if in.Status != nip.StatusRemoved {
			kept = append(kept, in)
		}
	}
	slices.SortFunc(kept, func(a, b nip.Institution) int { return strings.Compare(a.NIPCode, b.NIPCode) })
	start := 0
	if cursor != "" {
		i := slices.IndexFunc(kept, func(in nip.Institution) bool { return in.NIPCode == cursor })
		if i < 0 {
			return bankPage{}, errors.New("unknown cursor")
		}
		start = i + 1
	}
	end := min(start+limit, len(kept))
	page := bankPage{Data: kept[start:end]}
	if end < len(kept) {
		page.NextCursor = kept[end-1].NIPCode
	}
	return page, nil
}
