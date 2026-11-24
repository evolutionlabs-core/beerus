package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"math"
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
	"github.com/evolutionlabs-core/beerus/internal/config"
)

const reportChunk = 2

type nibssDesk struct {
	reports       []SettlementReport
	position      settlementPosition
	down          bool
	afters        []string
	positionAfter string
	onPosition    func()
}

type cbnDesk struct {
	balance  int64
	postings []cbnPosting
}

func (d cbnDesk) client() CBNClient {
	return CBNClient{Base: "http://cbn.invalid", Away: "Bank A cannot reach the CBN right now. Try again in a minute.", HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		var body any = cbnPostingPage{Data: append([]cbnPosting{}, d.postings...)}
		if strings.HasSuffix(r.URL.Path, "/settlement-balance") {
			body = map[string]int64{"balance_minor": d.balance}
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})}}
}

func (d *nibssDesk) client() NIBSSClient {
	return NIBSSClient{Base: "http://nibss.invalid", Away: "Bank A cannot reach NIBSS right now. Try again in a minute.", HTTP: &http.Client{Transport: roundTrip(d.serve)}}
}

func (d *nibssDesk) serve(r *http.Request) (*http.Response, error) {
	answer := func(status int, body string) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	if d.down {
		return answer(http.StatusServiceUnavailable, `{"error":{"code":"unavailable","message":"NIBSS is not available right now. Try again soon.","request_id":"req_1"}}`)
	}
	q := r.URL.Query()
	if r.URL.Path == positionPath {
		d.positionAfter = q.Get("after")
		if d.onPosition != nil {
			d.onPosition()
		}
		raw, err := json.Marshal(d.position)
		if err != nil {
			return nil, err
		}
		return answer(http.StatusOK, string(raw))
	}
	d.afters = append(d.afters, q.Get("after"))
	from := 0
	if c := q.Get("cursor"); c != "" {
		var err error
		if from, err = strconv.Atoi(c); err != nil {
			return nil, err
		}
	} else if after := q.Get("after"); after != "" {
		from = slices.IndexFunc(d.reports, func(x SettlementReport) bool { return x.SessionID == after }) + 1
	}
	page := reportPageBody{Data: []SettlementReport{}}
	end := min(from+reportChunk, len(d.reports))
	page.Data = append(page.Data, d.reports[from:end]...)
	if end < len(d.reports) {
		page.NextCursor = strconv.Itoa(end)
	}
	raw, err := json.Marshal(page)
	if err != nil {
		return nil, err
	}
	return answer(http.StatusOK, string(raw))
}

func report(key string, outward, inward int64) SettlementReport {
	return SettlementReport{SessionID: key, Outward: outward, Inward: inward, Net: inward - outward}
}

func reportBank(t *testing.T) *bank.Bank {
	t.Helper()
	b := bank.New(ledger.New(banktest.Schema(t), nil), bank.Profile{ID: "bank-a", Name: "Bank A", NIPCode: "999001", NUBANCode: "901"}, banktest.Fees(t))
	chk(t, b.Setup(t.Context()))
	return b
}

func booked(t *testing.T, b *bank.Bank) map[string]int64 {
	t.Helper()
	got, err := b.Settlements(t.Context())
	chk(t, err)
	return got
}

func TestBankPostsItsSideFromTheNIBSSReports(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cutoff := time.Date(2026, 11, 4, 12, 0, 0, 0, clock.Lagos)
	now := cutoff.Add(3 * time.Hour)
	all := []SettlementReport{report("k1", 500, 200), report("k2", 70, 0), report("k3", 0, 30)}
	fresh := reportBank(t)
	desk := &nibssDesk{reports: all}
	for _, s := range []struct {
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
		{"a net that is not inward less outward is refused", reportBank(t), func() {
			desk.down = false
			desk.reports = []SettlementReport{report("k1", 5, 0), {SessionID: "k2", Outward: 5, Inward: 2, Net: 9}}
		}, "", map[string]int64{"k1": -5}, "post settlement k2: outward 5, inward 2 and net 9 do not add up"},
		{"a net that overflows is refused", reportBank(t), func() {
			desk.reports = []SettlementReport{{SessionID: "k1", Outward: 5, Inward: math.MinInt64, Net: 0}}
		}, "", map[string]int64{}, "post settlement k1: outward 5, inward -9223372036854775808 and net 0 do not add up"},
	} {
		if s.before != nil {
			s.before()
		}
		desk.afters = nil
		err := PostSettlements(ctx, desk.client(), s.books, now)
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		if got := booked(t, s.books); !maps.Equal(got, s.want) || msg != s.err || (s.after != "" && (len(desk.afters) == 0 || desk.afters[0] != s.after)) {
			t.Errorf("%s: booked %v, error %q, asked after %q, want %v, %q and %q", s.name, got, msg, desk.afters, s.want, s.err, s.after)
		}
	}
	upgraded := reportBank(t)
	_, err := upgraded.Settle(ctx, "k1", 500, 200, cutoff)
	chk(t, err)
	upgradeDesk := &nibssDesk{reports: all[:2]}
	chk(t, PostSettlements(ctx, upgradeDesk.client(), upgraded, now))
	problems, perr := nibssProblems(ctx, upgradeDesk.client(), booked(t, upgraded), "bank-a")
	if got := booked(t, upgraded); !maps.Equal(got, map[string]int64{"k1": -300, "k2": -70}) || upgradeDesk.afters[0] != "k1" || perr != nil || len(problems) != 0 {
		t.Errorf("a bank the bridge already booked: %v after %q, problems %v, %v", got, upgradeDesk.afters, problems, perr)
	}
	odd := reportBank(t)
	_, err = odd.Settle(ctx, "k1", 500, 150, cutoff)
	chk(t, err)
	_, err = odd.Settle(ctx, "k9", 5, 0, cutoff.Add(time.Hour))
	chk(t, err)
	problems, err = nibssProblems(ctx, (&nibssDesk{reports: all[:2]}).client(), booked(t, odd), "bank-a")
	want := []string{
		"session k1 at bank-a: NIBSS nets -300, but the bank posted -350",
		"session k2 at bank-a: NIBSS nets -70, but the bank posted 0",
		"session k9 at bank-a: NIBSS nets 0, but the bank posted -5",
	}
	if err != nil || !slices.Equal(problems, want) {
		t.Errorf("problems: %v, %v, want %v", problems, err, want)
	}
}

func TestBookingErrorStillRecordsTheCheck(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	books := reportBank(t)
	cutoff := time.Date(2026, 11, 4, 12, 0, 0, 0, clock.Lagos)
	for _, key := range []string{"kx", "kz"} {
		_, err := books.Settle(ctx, key, 1, 0, cutoff)
		chk(t, err)
	}
	desk := &nibssDesk{reports: []SettlementReport{report("ky", 1, 0), report("kx", 2, 0)}}
	clk := clock.New()
	s := New(Config{Bank: config.Bank{ID: "bank-a", Name: "Bank A"}, Books: books, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Clock: clk})
	before := clk.Real()
	err := s.postAndCheck(ctx, desk.client(), cbnDesk{}.client())
	after := clk.Real()
	if !errors.Is(err, ledger.ErrIdempotency) || !strings.HasPrefix(err.Error(), "post settlement kx: ") {
		t.Errorf("the booking error: %v", err)
	}
	want := []string{
		"bank-a has NIP payable -3 and receivable 0, but its open NIBSS sessions come to 0 and 0",
		"session kx at bank-a: NIBSS nets -2, but the bank posted -1",
		"session kz at bank-a: NIBSS nets 0, but the bank posted -1",
		"bank-a's books show -3 at the CBN, but the CBN shows 0",
		"session kx at bank-a: the bank posted -1, but the CBN moved 0",
		"session ky at bank-a: the bank posted -1, but the CBN moved 0",
		"session kz at bank-a: the bank posted -1, but the CBN moved 0",
		"could not book session kx at bank-a: " + errors.Unwrap(err).Error(),
	}
	if !slices.Equal(s.settlementProblems, want) || s.settlementCheckedAt == nil || s.settlementCheckedAt.Before(before) || s.settlementCheckedAt.After(after) {
		t.Errorf("problems %q checked at %v, want %q between %v and %v", s.settlementProblems, s.settlementCheckedAt, want, before, after)
	}
}
