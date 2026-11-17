package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/bank"
	"github.com/evolutionlabs-core/beerus/internal/bank/banktest"
	"github.com/evolutionlabs-core/beerus/internal/bank/ledger"
	"github.com/evolutionlabs-core/beerus/internal/clock"
)

const reportChunk = 2

type nibssDesk struct {
	reports []SettlementReport
	down    bool
	afters  []string
}

func (d *nibssDesk) client() NIBSSClient {
	return NIBSSClient{Base: "https://nibss.invalid", Away: "Bank A cannot reach NIBSS right now. Try again in a minute.", HTTP: &http.Client{Transport: roundTrip(d.serve)}}
}

func (d *nibssDesk) serve(r *http.Request) (*http.Response, error) {
	answer := func(status int, body string) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	if d.down {
		return answer(http.StatusServiceUnavailable, `{"error":{"code":"unavailable","message":"NIBSS is not available right now. Try again soon.","request_id":"req_1"}}`)
	}
	q := r.URL.Query()
	d.afters = append(d.afters, q.Get("after"))
	from := 0
	if c := q.Get("cursor"); c != "" {
		from, _ = strconv.Atoi(c)
	} else if after := q.Get("after"); after != "" {
		from = slices.IndexFunc(d.reports, func(x SettlementReport) bool { return x.SessionID == after }) + 1
	}
	page := reportPageBody{Data: []SettlementReport{}}
	end := min(from+reportChunk, len(d.reports))
	page.Data = append(page.Data, d.reports[from:end]...)
	if end < len(d.reports) {
		page.NextCursor = strconv.Itoa(end)
	}
	raw, _ := json.Marshal(page)
	return answer(http.StatusOK, string(raw))
}

func report(key string, outward, inward int64) SettlementReport {
	return SettlementReport{SessionID: key, Outward: outward, Inward: inward, Net: inward - outward}
}

func reportBank(t *testing.T) *bank.Bank {
	t.Helper()
	b := bank.New(ledger.New(banktest.Schema(t), nil), bank.Profile{ID: "bank-a", Name: "Bank A", NIPCode: "999001", NUBANCode: "901"}, banktest.Fees(t))
	if err := b.Setup(t.Context()); err != nil {
		t.Fatal(err)
	}
	return b
}

func booked(t *testing.T, b *bank.Bank) map[string]int64 {
	t.Helper()
	got, err := b.Settlements(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestBankPostsItsSideFromTheNIBSSReports(t *testing.T) {
	ctx := context.Background()
	cutoff := time.Date(2026, 11, 4, 12, 0, 0, 0, clock.Lagos)
	now := cutoff.Add(3 * time.Hour)
	all := []SettlementReport{report("k1", 500, 200), report("k2", 70, 0), report("k3", 0, 30)}

	fresh := reportBank(t)
	desk := &nibssDesk{reports: all}
	steps := []struct {
		name   string
		books  *bank.Bank
		before func()
		after  string
		want   map[string]int64
		err    string
	}{
		{"first pass books every report across pages", fresh, nil, "", map[string]int64{"k1": -300, "k2": -70, "k3": 30}, ""},
		{"a replay asks after the newest and books nothing new", fresh, nil, "k3", map[string]int64{"k1": -300, "k2": -70, "k3": 30}, ""},
		{"a new report is booked alone", fresh, func() { desk.reports = append(desk.reports, report("k4", 5, 0)) }, "k3", map[string]int64{"k1": -300, "k2": -70, "k3": 30, "k4": -5}, ""},
		{"NIBSS down books nothing and says so", fresh, func() { desk.down = true; desk.reports = append(desk.reports, report("k5", 9, 0)) }, "", map[string]int64{"k1": -300, "k2": -70, "k3": 30, "k4": -5}, "read the settlement reports at NIBSS: NIBSS is not available right now. Try again soon."},
	}
	for _, s := range steps {
		if s.before != nil {
			s.before()
		}
		desk.afters = nil
		err := PostSettlements(ctx, desk.client(), s.books, now)
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		if got := booked(t, s.books); !equalNets(got, s.want) || msg != s.err {
			t.Errorf("%s: booked %v, error %q, want %v and %q", s.name, got, msg, s.want, s.err)
		}
		if s.after != "" && (len(desk.afters) == 0 || desk.afters[0] != s.after) {
			t.Errorf("%s: asked NIBSS after %q, want %q", s.name, desk.afters, s.after)
		}
	}

	upgraded := reportBank(t)
	if _, err := upgraded.Settle(ctx, "k1", 500, 200, cutoff); err != nil {
		t.Fatal(err)
	}
	upgradeDesk := &nibssDesk{reports: all[:2]}
	if err := PostSettlements(ctx, upgradeDesk.client(), upgraded, now); err != nil {
		t.Fatalf("a bank the bridge already booked: %v", err)
	}
	if got, want := booked(t, upgraded), map[string]int64{"k1": -300, "k2": -70}; !equalNets(got, want) || upgradeDesk.afters[0] != "k1" {
		t.Errorf("a bank the bridge already booked: %v after %q, want %v after k1", got, upgradeDesk.afters, want)
	}
	problems, err := nibssProblems(ctx, upgradeDesk.client(), booked(t, upgraded), "bank-a")
	if err != nil || len(problems) != 0 {
		t.Errorf("problems of a bank the bridge already booked: %v, %v", problems, err)
	}

	odd := reportBank(t)
	if _, err := odd.Settle(ctx, "k1", 500, 150, cutoff); err != nil {
		t.Fatal(err)
	}
	if _, err := odd.Settle(ctx, "k9", 5, 0, cutoff.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	oddDesk := &nibssDesk{reports: all[:2]}
	problems, err = nibssProblems(ctx, oddDesk.client(), booked(t, odd), "bank-a")
	want := []string{
		"session k1 at bank-a: NIBSS nets -300, but the bank posted -350",
		"session k2 at bank-a: NIBSS nets -70, but the bank posted 0",
		"session k9 at bank-a: NIBSS nets 0, but the bank posted -5",
	}
	if err != nil || !slices.Equal(problems, want) {
		t.Errorf("problems: %v, %v, want %v", problems, err, want)
	}
	stranger := reportBank(t)
	for _, key := range []string{"kx", "kz"} {
		if _, err := stranger.Settle(ctx, key, 1, 0, cutoff); err != nil {
			t.Fatal(err)
		}
	}
	err = PostSettlements(ctx, (&nibssDesk{reports: []SettlementReport{report("ky", 1, 0), report("kx", 2, 0)}}).client(), stranger, now)
	if !errors.Is(err, ledger.ErrIdempotency) || !strings.HasPrefix(err.Error(), "post settlement kx: ") {
		t.Errorf("a key booked with another amount: %v", err)
	}
	if got, want := booked(t, stranger), map[string]int64{"kx": -1, "kz": -1, "ky": -1}; !equalNets(got, want) {
		t.Errorf("after the clash: %v, want %v", got, want)
	}
}

func equalNets(a, b map[string]int64) bool {
	return len(a) == len(b) && func() bool {
		for k, v := range a {
			if w, ok := b[k]; !ok || w != v {
				return false
			}
		}
		return true
	}()
}
