package core

import (
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"github.com/evolutionlabs-core/beerus/internal/web"
)

func controlAt(routes map[string]string) *nibssAdmin {
	link := control.NewLink("", "")
	link.Client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		body, ok := routes[r.URL.Path]
		if !ok {
			res := stubReply(http.StatusNotFound, "404 page not found\n")
			res.Status = "404 Not Found"
			return res, nil
		}
		return stubReply(http.StatusOK, body), nil
	})}
	return &nibssAdmin{control.NewNIBSSControl(link, 1)}
}

func sessionsJSON(transfers int, outward, inward int64, unposted int) string {
	return `{"transfers":` + strconv.Itoa(transfers) + `,"outward_minor":` + strconv.FormatInt(outward, 10) + `,"inward_minor":` + strconv.FormatInt(inward, 10) + `,"unposted_settlements":` + strconv.Itoa(unposted) + `}`
}

func TestNIBSSStatusNeedsTheCountOfPostedSessions(t *testing.T) {
	for _, r := range []struct {
		name, body string
		posted     int
		problems   []string
		err        string
	}{
		{"counted with a problem", `{"posted_sessions":3,"problems":["not posted to the CBN yet"]}`, 3, []string{"not posted to the CBN yet"}, ""},
		{"counted and clean", `{"posted_sessions":0,"problems":[]}`, 0, nil, ""},
		{"not counted", `{"problems":["NIBSS could not check its settlements: read settlement reports"]}`, 0, nil, "NIBSS could not count its settlement sessions: NIBSS could not check its settlements: read settlement reports"},
	} {
		posted, problems, err := controlAt(map[string]string{nibssStatusPath: r.body}).status(t.Context())
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		if posted != r.posted || !slices.Equal(problems, r.problems) || msg != r.err {
			t.Errorf("%s: %d %q %q, want %d %q %q", r.name, posted, problems, msg, r.posted, r.problems, r.err)
		}
	}
}

func TestOpenSessionsDecideWhatClosingWaitsFor(t *testing.T) {
	const reported = `{"next_cutoff_at":"2026-11-04T11:00:00Z"}`
	waiting := func(cutoff, what string) string {
		return "Waiting for the " + cutoff + " NIP session to settle " + what + ". Wait for it, or run beerus nip settle-now."
	}
	a := &App{}
	for _, r := range []struct {
		name, body, status, want, err string
	}{
		{"nothing open", sessionsJSON(0, 0, 0, 0), "", "", ""},
		{"transfers still to settle", sessionsJSON(2, 500000, 270000, 0), "", waiting("12:00", "2 transfers (net ₦2,300.00)"), ""},
		{"one transfer to settle", sessionsJSON(1, 0, 10000, 0), "", waiting("12:00", "1 transfer (net ₦100.00)"), ""},
		{"largest inward", sessionsJSON(1, 0, math.MaxInt64, 0), "", waiting("12:00", "1 transfer (net ₦92,233,720,368,547,758.07)"), ""},
		{"settlements not posted", sessionsJSON(0, 0, 0, 2), "", "Waiting for 2 settlements with bank-a to be posted to the books. Try again soon.", ""},
		{"negative amounts", sessionsJSON(1, -1, 0, 0), "", "", "the open NIP sessions of bank-a do not add up"},
		{"NIBSS gives no cutoff", sessionsJSON(1, 0, 10000, 0), `{}`, "", "NIBSS has not said when the next NIP session is"},
		{"cutoff after UTC midnight", sessionsJSON(1, 0, 10000, 0), `{"next_cutoff_at":"2026-11-04T23:00:00Z"}`, waiting("00:00", "1 transfer (net ₦100.00)"), ""},
	} {
		if r.status == "" {
			r.status = reported
		}
		a.nibssCtl = controlAt(map[string]string{nibssBanksPath + "bank-a" + nibssOpenSuffix: r.body, nibssStatusPath: r.status})
		got, err := a.inflightWith(t.Context(), "bank-a", 0)
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		if got != r.want || msg != r.err {
			t.Errorf("%s: %q, %v, want %q, %q", r.name, got, err, r.want, r.err)
		}
	}
	a.nibssCtl = nil
	if _, err := a.inflightWith(t.Context(), "bank-a", 0); err == nil || err.Error() != nibssAway {
		t.Errorf("NIBSS not open: %v", err)
	}
	for sends, want := range map[int]string{1: "Bank A has 1 transfer still being sent. Wait for it to finish.", 2: "Bank A has 2 transfers still being sent. Wait for them to finish."} {
		a := newTestApp(t, Options{})
		if got, err := a.inflightWith(t.Context(), "bank-a", sends); err != nil || got != want {
			t.Errorf("%d open sends: %q, %v, want %q", sends, got, err, want)
		}
	}
}

func positionsAt(status string) *httptest.ResponseRecorder {
	open := sessionsJSON(0, 0, 0, 0)
	routes := map[string]string{nibssBanksPath + "bank-a" + nibssOpenSuffix: open, nibssBanksPath + "bank-b" + nibssOpenSuffix: open, nibssStatusPath: status}
	a := &App{log: slog.New(slog.DiscardHandler), nibssCtl: controlAt(routes), cfg: &config.Config{Banks: []*config.Bank{{ID: "bank-a"}, {ID: "bank-b"}}}}
	w := httptest.NewRecorder()
	a.ctlNIPPositions(w, httptest.NewRequest(http.MethodGet, "/", nil))
	return w
}

func TestSettlementAnswers(t *testing.T) {
	a := &App{log: slog.New(slog.DiscardHandler), link: control.NewLink("", ""), clock: clock.New()}
	if _, err := a.freshBankProblems(t.Context(), &config.Bank{ID: "bank-a", Name: "Bank A", Port: 1}, a.clock.Real()); err == nil || err.Error() != "Bank A is not answering, so its books were not checked" {
		t.Fatalf("a bank that is down: %v", err)
	}
	cutoff := &App{log: slog.New(slog.DiscardHandler), nibssCtl: controlAt(map[string]string{nibssBanksPath + "bank-a" + nibssOpenSuffix: sessionsJSON(1, 0, 10000, 0), nibssStatusPath: `{}`})}
	_, noCutoff := cutoff.inflightWith(t.Context(), "bank-a", 0)
	closing := httptest.NewRecorder()
	cutoff.bankError(closing, httptest.NewRequest(http.MethodGet, "/", nil), noCutoff)
	for _, r := range []struct {
		name   string
		w      *httptest.ResponseRecorder
		status int
		want   string
	}{
		{"positions in a Monday cutoff after a weekend", positionsAt(`{"next_cutoff_at":"2026-11-09T02:00:00+01:00"}`), 200, `"next_session":"2026-11-09T02:00:00+01:00"`},
		{"positions show a UTC cutoff in Lagos", positionsAt(`{"next_cutoff_at":"2026-12-31T23:00:00Z"}`), 200, `"next_session":"2027-01-01T00:00:00+01:00"`},
		{"positions without a cutoff", positionsAt(`{}`), 503, "NIBSS has not said when the next NIP session is. Try again soon."},
		{"close preview without a cutoff", closing, 503, "NIBSS has not said when the next NIP session is. Try again soon."},
	} {
		if r.w.Code != r.status || !strings.Contains(r.w.Body.String(), r.want) {
			t.Errorf("%s: %d %s, want %d containing %q", r.name, r.w.Code, r.w.Body.String(), r.status, r.want)
		}
	}
	for _, r := range []struct {
		name   string
		err    error
		status int
		want   string
	}{
		{"not compared yet", &uncheckedError{Name: "Bank B", Why: "has not compared its books with the CBN yet"}, 503, "Bank B has not compared its books with the CBN yet. Try again soon."},
		{"NIBSS not open", &control.UnavailableError{Msg: nibssAway}, 503, nibssAway},
		{"something else", errors.New("disk full"), 500, web.SomethingWentWrong},
	} {
		w := httptest.NewRecorder()
		a.settlementFailed(w, httptest.NewRequest(http.MethodGet, "/", nil), "check settlement", r.err)
		if w.Code != r.status || !strings.Contains(w.Body.String(), r.want) {
			t.Errorf("%s: %d %s, want %d containing %q", r.name, w.Code, w.Body.String(), r.status, r.want)
		}
	}
}
