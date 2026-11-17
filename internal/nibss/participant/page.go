package participant

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/evolutionlabs-core/beerus/internal/web"
)

const (
	defaultLimit = 50
	maxLimit     = 500
)

func pageLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return defaultLimit, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > maxLimit {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", fmt.Sprintf("limit must be a number from 1 to %d.", maxLimit))
		return 0, false
	}
	return n, true
}

func badCursor(w http.ResponseWriter, r *http.Request) {
	web.WriteError(w, r, http.StatusBadRequest, "invalid_cursor", "That cursor is not valid. Use the next_cursor value from the last page.")
}
