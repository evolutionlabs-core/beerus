package connect

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/world"
)

func TestPageNumbersAreBoundedBeforeTheyAreUsed(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, clock.Lagos)
	st := newState()
	st.Deliveries = []*Delivery{{ID: "dlv_1"}, {ID: "dlv_2"}, {ID: "dlv_3"}}
	a := &App{
		cfg:   &config.Config{},
		st:    st,
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		clock: clock.New(clock.State{Pace: 1}, func() time.Time { return now }),
		world: world.New(world.Config{Seed: 42, Genesis: clock.DateOf(now).AddDays(-30)}, world.Overrides{}),
	}
	cases := []struct {
		page        string
		wantPage    int
		wantPageErr int
		wantWebhook int
	}{
		{"", 1, 0, http.StatusOK},
		{"0", 1, 0, http.StatusOK},
		{"-1", 1, 0, http.StatusOK},
		{"1", 1, 0, http.StatusOK},
		{"2", 2, 0, http.StatusNotFound},
		{"3", 0, http.StatusUnprocessableEntity, http.StatusUnprocessableEntity},
		{"300000000000000000", 0, http.StatusUnprocessableEntity, http.StatusUnprocessableEntity},
		{"9223372036854775807", 0, http.StatusUnprocessableEntity, http.StatusUnprocessableEntity},
		{"99999999999999999999", 0, http.StatusUnprocessableEntity, http.StatusUnprocessableEntity},
		{"abc", 0, http.StatusUnprocessableEntity, http.StatusUnprocessableEntity},
	}
	for _, c := range cases {
		t.Run(c.page, func(t *testing.T) {
			target := "/ui/webhooks?page=" + c.page
			rec := httptest.NewRecorder()
			page, ok := pageParam(rec, httptest.NewRequest(http.MethodGet, target, nil), "webhook list", pagesOf(len(st.Deliveries), pageSize)+1)
			if c.wantPageErr == 0 {
				if !ok || page != c.wantPage {
					t.Fatalf("page %q gave %d, %v", c.page, page, ok)
				}
			} else if ok || rec.Code != c.wantPageErr {
				t.Fatalf("page %q gave %d, %v, status %d", c.page, page, ok, rec.Code)
			}
			rec = httptest.NewRecorder()
			a.roomWebhooks(rec, httptest.NewRequest(http.MethodGet, target, nil))
			if rec.Code != c.wantWebhook {
				t.Fatalf("webhooks page %q gave status %d", c.page, rec.Code)
			}
		})
	}

	rec := httptest.NewRecorder()
	pageParam(rec, httptest.NewRequest(http.MethodGet, "/apps?page=300000000000000000", nil), "access history", maxHistoryPages)
	if want := "The page 300000000000000000 is too high. The access history allows pages 1 to 1000.\n"; rec.Body.String() != want {
		t.Fatalf("message %q, want %q", rec.Body.String(), want)
	}
	if got := pagesOf(0, 50); got != 1 {
		t.Fatalf("an empty list has %d pages, want 1", got)
	}
	if got := pagesOf(101, 50); got != 3 {
		t.Fatalf("101 rows have %d pages, want 3", got)
	}
}
