package connect

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/view"
)

func TestPageNumbersAreBoundedBeforeTheyAreUsed(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, clock.Lagos)
	st := newState()
	st.Deliveries = []*Delivery{{ID: "dlv_1"}, {ID: "dlv_2"}, {ID: "dlv_3"}}
	a := &App{
		cfg:   &config.Config{},
		st:    st,
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		clock: clock.New(func() time.Time { return now }),
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
		{"2", 0, http.StatusUnprocessableEntity, http.StatusUnprocessableEntity},
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
			page, ok := view.PageParam(rec, httptest.NewRequest(http.MethodGet, target, nil), "webhook list", view.PagesOf(len(st.Deliveries), pageSize))
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

	if got := view.PagesOf(0, 50); got != 1 {
		t.Fatalf("an empty list has %d pages, want 1", got)
	}
	if got := view.PagesOf(101, 50); got != 3 {
		t.Fatalf("101 rows have %d pages, want 3", got)
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

func TestOpenSendsBlockClosing(t *testing.T) {
	a := newTestApp(t, Options{})
	b := a.bankByID("bank-a")
	for sends, want := range map[int]string{
		1: "Bank A has 1 transfer still being sent. Wait for it to finish.",
		2: "Bank A has 2 transfers still being sent. Wait for them to finish.",
	} {
		got, err := a.inflightWith(t.Context(), b.NIPCode, "bank-a", sends)
		if err != nil || got != want {
			t.Fatalf("%d open sends: %q, %v, want %q", sends, got, err, want)
		}
	}
}
