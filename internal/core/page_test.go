package core

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"github.com/evolutionlabs-core/beerus/internal/view"
)

func TestPageNumbersAreBoundedBeforeTheyAreUsed(t *testing.T) {
	const deliveries = 3
	link := control.NewLink("token", "")
	link.Client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/status" {
			return stubReply(http.StatusOK, fmt.Sprintf(`{"deliveries":%d}`, deliveries)), nil
		}
		return stubReply(http.StatusOK, `{"data":[]}`), nil
	})}
	a := &App{cfg: &config.Config{}, st: newState(), log: slog.New(slog.DiscardHandler), clock: clock.New(), agg: control.ConnectControl{Link: link, Base: "http://connect"}}
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
		{"-1", "1", http.StatusOK},
		{"1", "1", http.StatusOK},
		{"2", "", http.StatusUnprocessableEntity},
		{"9223372036854775807", "", http.StatusUnprocessableEntity},
		{"99999999999999999999", "", http.StatusUnprocessableEntity},
		{"abc", "", http.StatusUnprocessableEntity},
	} {
		query := "/?page=" + url.QueryEscape(c.page)
		rec := httptest.NewRecorder()
		paged(rec, httptest.NewRequest(http.MethodGet, query, nil))
		if rec.Code != c.wantStatus || (c.wantStatus == http.StatusOK && rec.Body.String() != c.wantPage) {
			t.Errorf("page %q gave %d %q, want %d %q", c.page, rec.Code, rec.Body.String(), c.wantStatus, c.wantPage)
		}
		rec = httptest.NewRecorder()
		a.roomWebhooks(rec, httptest.NewRequest(http.MethodGet, query, nil))
		if rec.Code != c.wantStatus {
			t.Errorf("webhooks page %q gave status %d, want %d", c.page, rec.Code, c.wantStatus)
		}
	}
	if view.PagesOf(0, 50) != 1 || view.PagesOf(101, 50) != 3 {
		t.Fatalf("an empty list has %d pages and 101 rows have %d, want 1 and 3", view.PagesOf(0, 50), view.PagesOf(101, 50))
	}
}

func TestTransferFormCarriesAFreshKey(t *testing.T) {
	a := newTestApp(t, Options{})
	hidden := regexp.MustCompile(`name="key" value="(idk_[^"]+)"`)
	seen := map[string]bool{}
	for range 2 {
		rec := httptest.NewRecorder()
		a.roomTransferForm(rec, httptest.NewRequest(http.MethodGet, "/ui/transfer", nil))
		m := hidden.FindStringSubmatch(rec.Body.String())
		if rec.Code != http.StatusOK || m == nil {
			t.Fatalf("transfer form: status %d, key field %v", rec.Code, m)
		}
		seen[m[1]] = true
	}
	if len(seen) != 2 {
		t.Fatalf("two renders gave %d different keys, want 2", len(seen))
	}
}
