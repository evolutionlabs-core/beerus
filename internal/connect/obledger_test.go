package connect

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

func TestConsentLedgerPages(t *testing.T) {
	cfg := initTestConfig(t, clock.Date{Y: 2026, M: 11, D: 3})
	app, err := New(cfg, Options{WorldOnly: true, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 25; i++ {
		bank := "bank-a"
		if i%5 == 0 {
			bank = "bank-b"
		}
		app.st.ConsentLedger = append(app.st.ConsentLedger, ConsentRecord{Seq: int64(i), ConsentID: fmt.Sprint(i), Bank: bank})
	}
	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		url := "/registry/consents?bank=bank-a&limit=7&cursor=" + cursor
		rec := httptest.NewRecorder()
		app.ctlConsentLedger(rec, httptest.NewRequest(http.MethodGet, url, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("page %d: %d %s", pages, rec.Code, rec.Body)
		}
		var body struct {
			Data []ConsentRecord `json:"data"`
			Next string          `json:"next"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		for _, e := range body.Data {
			if seen[e.ConsentID] || e.Bank != "bank-a" {
				t.Fatalf("repeated or foreign entry %+v", e)
			}
			seen[e.ConsentID] = true
		}
		pages++
		if body.Next == "" {
			break
		}
		if len(body.Data) != 7 {
			t.Fatalf("short page with a next cursor: %d", len(body.Data))
		}
		cursor = body.Next
	}
	if len(seen) != 20 || pages != 3 {
		t.Fatalf("saw %d entries in %d pages", len(seen), pages)
	}
	for _, q := range []string{"limit=0", "limit=x", "limit=100000", "cursor=abc", "cursor=0"} {
		rec := httptest.NewRecorder()
		app.ctlConsentLedger(rec, httptest.NewRequest(http.MethodGet, "/registry/consents?"+q, nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", q, rec.Code)
		}
	}
}

func TestEventClientIsSharedPerDestination(t *testing.T) {
	cfg := initTestConfig(t, clock.Date{Y: 2026, M: 11, D: 3})
	app, err := New(cfg, Options{WorldOnly: true, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.eventClient("https://widget-a.localhost")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		again, err := app.eventClient("https://widget-a.localhost")
		if err != nil || again != a {
			t.Fatalf("attempt %d built a new client: %v", i, err)
		}
	}
	b, err := app.eventClient("https://widget-b.localhost")
	if err != nil || b == a {
		t.Fatalf("another destination shares a client: %v", err)
	}
	if tr, ok := a.Transport.(*http.Transport); !ok || tr.IdleConnTimeout <= 0 {
		t.Fatalf("idle connections are never closed: %+v", a.Transport)
	}
}
