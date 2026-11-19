package core

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"github.com/evolutionlabs-core/beerus/internal/view"
)

func TestPageNumbersAreBoundedBeforeTheyAreUsed(t *testing.T) {
	const deliveries = 3
	connectBase := serveFor(t, map[string]http.HandlerFunc{
		"/v1/status":                      func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"deliveries":%d}`, deliveries) },
		"/v1/control/webhooks/deliveries": func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":[]}`) },
	})
	a := &App{
		cfg:   &config.Config{},
		st:    newState(),
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		clock: clock.New(),
		agg:   control.ConnectControl{Link: control.NewLink("token", ""), Base: connectBase},
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
	paged := func(w http.ResponseWriter, r *http.Request) {
		page, ok := view.PageParam(w, r, "webhook list", view.PagesOf(deliveries, pageSize))
		if ok {
			fmt.Fprint(w, page)
		}
	}
	base := serveFor(t, map[string]http.HandlerFunc{"/paged": paged, "/webhooks": a.roomWebhooks})
	for _, c := range cases {
		t.Run(c.page, func(t *testing.T) {
			query := "?page=" + url.QueryEscape(c.page)
			code, body := fetch(t, base+"/paged"+query)
			if c.wantPageErr == 0 {
				if code != http.StatusOK || body != fmt.Sprint(c.wantPage) {
					t.Fatalf("page %q gave %d %q, want page %d", c.page, code, body, c.wantPage)
				}
			} else if code != c.wantPageErr {
				t.Fatalf("page %q gave status %d, want %d", c.page, code, c.wantPageErr)
			}
			if code, _ := fetch(t, base+"/webhooks"+query); code != c.wantWebhook {
				t.Fatalf("webhooks page %q gave status %d", c.page, code)
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

func serveFor(t *testing.T, routes map[string]http.HandlerFunc) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	for path, h := range routes {
		mux.HandleFunc(path, h)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go srv.Serve(ln)
	t.Cleanup(func() { _ = srv.Close() })
	return "http://" + ln.Addr().String()
}

func fetch(t *testing.T, target string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(raw)
}

func TestTransferFormCarriesAFreshKey(t *testing.T) {
	a := newTestApp(t, Options{})
	base := serveFor(t, map[string]http.HandlerFunc{"/ui/transfer": a.roomTransferForm})
	hidden := regexp.MustCompile(`name="key" value="(idk_[^"]+)"`)
	seen := map[string]bool{}
	for range 2 {
		code, body := fetch(t, base+"/ui/transfer")
		m := hidden.FindStringSubmatch(body)
		if code != http.StatusOK || m == nil {
			t.Fatalf("transfer form: status %d, key field %v", code, m)
		}
		seen[m[1]] = true
	}
	if len(seen) != 2 {
		t.Fatalf("two renders gave %d different keys, want 2", len(seen))
	}
}

func TestOpenSendsBlockClosing(t *testing.T) {
	a := newTestApp(t, Options{})
	for sends, want := range map[int]string{
		1: "Bank A has 1 transfer still being sent. Wait for it to finish.",
		2: "Bank A has 2 transfers still being sent. Wait for them to finish.",
	} {
		got, err := a.inflightWith(t.Context(), "bank-a", sends)
		if err != nil || got != want {
			t.Fatalf("%d open sends: %q, %v, want %q", sends, got, err, want)
		}
	}
}
