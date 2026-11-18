package core

import (
	"encoding/json"
	"errors"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

func controlAt(t *testing.T, routes map[string]string) *nibssAdmin {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	for path, body := range routes {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if _, err := w.Write([]byte(body)); err != nil {
				t.Error(err)
			}
		})
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.Serve(l); !errors.Is(err, http.ErrServerClosed) {
			t.Error(err)
		}
	}()
	t.Cleanup(func() {
		if err := srv.Close(); err != nil {
			t.Error(err)
		}
		<-done
	})
	port := l.Addr().(*net.TCPAddr).Port
	return &nibssAdmin{control.NewNIBSSControl(control.NewLink("", ""), port)}
}

func TestNIBSSStatusNeedsTheCountOfPostedSessions(t *testing.T) {
	rows := []struct {
		name     string
		body     string
		posted   int
		problems []string
		err      string
	}{
		{"counted with a problem", `{"posted_sessions":3,"problems":["settlement session 2026-11-04T12:00 was cut off at 2026-11-04T12:00:00+01:00 and is not posted to the CBN yet"]}`, 3, []string{"settlement session 2026-11-04T12:00 was cut off at 2026-11-04T12:00:00+01:00 and is not posted to the CBN yet"}, ""},
		{"counted and clean", `{"posted_sessions":0,"problems":[]}`, 0, []string{}, ""},
		{"not counted", `{"problems":["NIBSS could not check its settlements: read settlement reports"]}`, 0, nil, "NIBSS could not count its settlement sessions: NIBSS could not check its settlements: read settlement reports"},
	}
	for _, r := range rows {
		n := controlAt(t, map[string]string{nibssStatusPath: r.body})
		posted, problems, err := n.status(t.Context())
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		if posted != r.posted || !equalLines(problems, r.problems) || msg != r.err {
			t.Errorf("%s: %d %q %q, want %d %q %q", r.name, posted, problems, msg, r.posted, r.problems, r.err)
		}
	}
}

func equalLines(a, b []string) bool {
	if len(a) != len(b) || (a == nil) != (b == nil) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestOpenSessionsDecideWhatClosingWaitsFor(t *testing.T) {
	a := newTestApp(t, Options{})
	const cutoff = "12:00"
	const reported = `{"next_cutoff_at":"2026-11-04T11:00:00Z"}`
	live := a.nibssCtl
	t.Cleanup(func() { a.nibssCtl = live })
	rows := []struct {
		name   string
		body   string
		status string
		down   bool
		want   string
		err    string
	}{
		{"nothing open", `{"transfers":0,"outward_minor":0,"inward_minor":0,"unposted_settlements":0}`, "", false, "", ""},
		{"transfers still to settle", `{"transfers":2,"outward_minor":500000,"inward_minor":270000,"unposted_settlements":0}`, "", false, "Waiting for the " + cutoff + " NIP session to settle 2 transfers (net ₦2,300.00). Wait for it, or run beerus nip settle-now.", ""},
		{"one transfer to settle", `{"transfers":1,"outward_minor":0,"inward_minor":10000,"unposted_settlements":0}`, "", false, "Waiting for the " + cutoff + " NIP session to settle 1 transfer (net ₦100.00). Wait for it, or run beerus nip settle-now.", ""},
		{"settlements not posted", `{"transfers":0,"outward_minor":0,"inward_minor":0,"unposted_settlements":2}`, "", false, "Waiting for 2 settlements with bank-a to be posted to the books. Try again soon.", ""},
		{"one settlement not posted", `{"transfers":0,"outward_minor":0,"inward_minor":0,"unposted_settlements":1}`, "", false, "Waiting for 1 settlement with bank-a to be posted to the books. Try again soon.", ""},
		{"negative outward", `{"transfers":1,"outward_minor":-1,"inward_minor":0,"unposted_settlements":0}`, "", false, "", "the open NIP sessions of bank-a do not add up"},
		{"negative inward", `{"transfers":1,"outward_minor":0,"inward_minor":-1,"unposted_settlements":0}`, "", false, "", "the open NIP sessions of bank-a do not add up"},
		{"largest inward", `{"transfers":1,"outward_minor":0,"inward_minor":` + strconv.FormatInt(math.MaxInt64, 10) + `,"unposted_settlements":0}`, "", false, "Waiting for the " + cutoff + " NIP session to settle 1 transfer (net ₦92,233,720,368,547,758.07). Wait for it, or run beerus nip settle-now.", ""},
		{"NIBSS gives no cutoff", `{"transfers":1,"outward_minor":0,"inward_minor":10000,"unposted_settlements":0}`, `{}`, false, "", "NIBSS has not said when the next NIP session is"},
		{"cutoff after UTC midnight", `{"transfers":1,"outward_minor":0,"inward_minor":10000,"unposted_settlements":0}`, `{"next_cutoff_at":"2026-11-04T23:00:00Z"}`, false, "Waiting for the 00:00 NIP session to settle 1 transfer (net ₦100.00). Wait for it, or run beerus nip settle-now.", ""},
		{"NIBSS not open", ``, "", true, "", nibssAway},
	}
	for _, r := range rows {
		status := r.status
		if status == "" {
			status = reported
		}
		a.nibssCtl = controlAt(t, map[string]string{nibssBanksPath + "bank-a" + nibssOpenSuffix: r.body, nibssStatusPath: status})
		if r.down {
			a.nibssCtl = nil
		}
		got, err := a.inflightWith(t.Context(), "bank-a", 0)
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		if got != r.want || msg != r.err {
			t.Errorf("%s: %q, %v, want %q, %q", r.name, got, err, r.want, r.err)
		}
	}
}

func TestUncheckedBankAnswersUnavailable(t *testing.T) {
	a := &App{log: slog.New(slog.DiscardHandler), link: control.NewLink("", ""), clock: clock.New()}
	down := &config.Bank{ID: "bank-a", Name: "Bank A", Port: 1}
	_, err := a.freshBankProblems(t.Context(), down, a.clock.Real())
	var unchecked *uncheckedError
	if !errors.As(err, &unchecked) || unchecked.Error() != "Bank A is not answering, so its books were not checked" || unchecked.Unwrap() == nil {
		t.Fatalf("a bank that is down: %v", err)
	}
	rows := []struct {
		name    string
		err     error
		status  int
		code    string
		message string
	}{
		{"not answering", &uncheckedError{Name: "Bank A", Why: "is not answering, so its books were not checked"}, http.StatusServiceUnavailable, "unavailable", "Bank A is not answering, so its books were not checked. Try again soon."},
		{"not compared yet", &uncheckedError{Name: "Bank B", Why: "has not compared its books with the CBN yet"}, http.StatusServiceUnavailable, "unavailable", "Bank B has not compared its books with the CBN yet. Try again soon."},
		{"NIBSS not open", &control.UnavailableError{Msg: nibssAway}, http.StatusServiceUnavailable, "unavailable", nibssAway},
		{"something else", errors.New("disk full"), http.StatusInternalServerError, "internal_error", web.SomethingWentWrong},
	}
	for _, r := range rows {
		w := httptest.NewRecorder()
		a.settlementFailed(w, httptest.NewRequest(http.MethodGet, "/", nil), "check settlement", r.err)
		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: %v", r.name, err)
		}
		if w.Code != r.status || body.Error.Code != r.code || body.Error.Message != r.message {
			t.Errorf("%s: %d %s %q, want %d %s %q", r.name, w.Code, body.Error.Code, body.Error.Message, r.status, r.code, r.message)
		}
	}
}

func positionsAt(t *testing.T, status string) *httptest.ResponseRecorder {
	t.Helper()
	const open = `{"transfers":0,"outward_minor":0,"inward_minor":0,"unposted_settlements":0}`
	routes := map[string]string{nibssBanksPath + "bank-a" + nibssOpenSuffix: open, nibssBanksPath + "bank-b" + nibssOpenSuffix: open, nibssStatusPath: status}
	a := &App{log: slog.New(slog.DiscardHandler), nibssCtl: controlAt(t, routes), cfg: &config.Config{Banks: []*config.Bank{{ID: "bank-a"}, {ID: "bank-b"}}}}
	w := httptest.NewRecorder()
	a.ctlNIPPositions(w, httptest.NewRequest(http.MethodGet, "/", nil))
	return w
}

func TestPositionsTakeTheNextSessionFromNIBSS(t *testing.T) {
	rows := []struct {
		name   string
		status string
		want   string
	}{
		{"a Monday cutoff after a weekend", `{"next_cutoff_at":"2026-11-09T02:00:00+01:00"}`, "2026-11-09T02:00:00+01:00"},
		{"a cutoff in UTC is shown in Lagos", `{"next_cutoff_at":"2026-12-31T23:00:00Z"}`, "2027-01-01T00:00:00+01:00"},
	}
	for _, r := range rows {
		w := positionsAt(t, r.status)
		var body struct {
			Data []struct {
				BankID string `json:"bank_id"`
				Next   string `json:"next_session"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: %v", r.name, err)
		}
		if w.Code != http.StatusOK || len(body.Data) != 2 || body.Data[0].Next != r.want || body.Data[1].Next != r.want {
			t.Errorf("%s: %d %+v, want both banks at %q", r.name, w.Code, body.Data, r.want)
		}
	}
}

func TestMissingCutoffAnswersUnavailable(t *testing.T) {
	const want = "NIBSS has not said when the next NIP session is. Try again soon."
	const busy = `{"transfers":1,"outward_minor":0,"inward_minor":10000,"unposted_settlements":0}`
	a := &App{log: slog.New(slog.DiscardHandler), nibssCtl: controlAt(t, map[string]string{nibssBanksPath + "bank-a" + nibssOpenSuffix: busy, nibssStatusPath: `{}`})}
	_, err := a.inflightWith(t.Context(), "bank-a", 0)
	closing := httptest.NewRecorder()
	a.bankError(closing, httptest.NewRequest(http.MethodGet, "/", nil), err)
	rows := []struct {
		name string
		w    *httptest.ResponseRecorder
	}{
		{"positions", positionsAt(t, `{}`)},
		{"close preview", closing},
	}
	for _, r := range rows {
		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(r.w.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: %v", r.name, err)
		}
		if r.w.Code != http.StatusServiceUnavailable || body.Error.Code != "unavailable" || body.Error.Message != want {
			t.Errorf("%s: %d %s %q, want 503 unavailable %q", r.name, r.w.Code, body.Error.Code, body.Error.Message, want)
		}
	}
}
