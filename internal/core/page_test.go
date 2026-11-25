package core

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/view"
)

func TestPageNumbersAreBoundedBeforeTheyAreUsed(t *testing.T) {
	const deliveries = 3
	paged := func(w http.ResponseWriter, r *http.Request) {
		if page, ok := view.PageParam(w, r, "webhook list", view.PagesOf(deliveries, pageSize)); ok {
			fmt.Fprint(w, page)
		}
	}
	for _, c := range []struct {
		page       string
		wantPage   string
		wantStatus int
	}{
		{"", "1", http.StatusOK},
		{"0", "1", http.StatusOK},
		{"2", "", http.StatusUnprocessableEntity},
		{"9223372036854775807", "", http.StatusUnprocessableEntity},
		{"abc", "", http.StatusUnprocessableEntity},
	} {
		query := "/?page=" + url.QueryEscape(c.page)
		rec := httptest.NewRecorder()
		paged(rec, httptest.NewRequest(http.MethodGet, query, nil))
		if rec.Code != c.wantStatus || (c.wantStatus == http.StatusOK && rec.Body.String() != c.wantPage) {
			t.Errorf("page %q gave %d %q, want %d %q", c.page, rec.Code, rec.Body.String(), c.wantStatus, c.wantPage)
		}
	}
	if view.PagesOf(0, 50) != 1 || view.PagesOf(101, 50) != 3 {
		t.Fatalf("an empty list has %d pages and 101 rows have %d, want 1 and 3", view.PagesOf(0, 50), view.PagesOf(101, 50))
	}
}
