package site

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

func TestLoginShowsTestUsernames(t *testing.T) {
	rows := []struct {
		name  string
		users []string
		want  string
	}{
		{"one customer", []string{"ada"}, `Test username <span class="font-mono text-ink">ada</span></p>`},
		{"several are sorted", []string{"chidi", "bayo", "ada"}, `Test usernames <span class="font-mono text-ink">ada, bayo, chidi</span></p>`},
		{"more than the limit", []string{"g", "f", "e", "d", "c", "b", "a"}, `Test usernames <span class="font-mono text-ink">a, b, c, d, e</span> and 2 more</p>`},
		{"no customers", nil, ""},
	}
	hash, err := bank.HashPassword("pw")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			books := newBooks(t)
			at := time.Date(2026, time.March, 2, 9, 0, 0, 0, time.UTC)
			for i, u := range r.users {
				bvn := fmt.Sprintf("2290000000%d", i)
				if _, err := books.CreateCustomer(context.Background(), "key-"+u, u, "+2348030000001", clock.Date{Y: 1990, M: time.May, D: 14}, bvn, u, hash, at); err != nil {
					t.Fatal(err)
				}
			}
			h := New(Config{Host: fakeHost{phase: config.PhaseActive, books: books}, Clock: clock.New(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
			req, err := http.NewRequest(http.MethodGet, "/login", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Host = "bank-a.localhost:7000"
			req.RemoteAddr = "127.0.0.1:50000"
			w := &capture{header: http.Header{}}
			h.ServeHTTP(w, req)
			body := w.body.String()
			if w.status != http.StatusOK {
				t.Fatalf("status %d, want 200", w.status)
			}
			if r.want == "" {
				if strings.Contains(body, "Test username") {
					t.Fatal("page shows a note with no customers")
				}
				return
			}
			if !strings.Contains(body, r.want) {
				t.Fatalf("page lacks %q", r.want)
			}
		})
	}
}
