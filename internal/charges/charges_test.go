package charges

import (
	"math"
	"strings"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/countrypack"
)

func nigeria(t *testing.T) *Schedule {
	t.Helper()
	pack, err := countrypack.Nigeria()
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(pack.Fees)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNIPFeeOn(t *testing.T) {
	s := nigeria(t)
	d := clock.Date{Y: 2026, M: 1, D: 1}
	cases := map[string]struct {
		amount int64
		d      clock.Date
		want   int64
	}{
		"zero":               {0, d, 1000},
		"top of first band":  {500000, d, 1000},
		"above first band":   {500001, d, 2500},
		"top of second band": {5000000, d, 2500},
		"above second band":  {5000001, d, 5000},
		"max amount":         {math.MaxInt64, d, 5000},
		"before first rule":  {500001, clock.Date{Y: 2019, M: 12, D: 31}, 2500},
		"first rule day":     {500001, clock.Date{Y: 2020, M: 1, D: 1}, 2500},
		"far future":         {500001, clock.Date{Y: 2300, M: 2, D: 29}, 1000},
	}
	for name, c := range cases {
		if got := s.NIPFeeOn(c.amount, c.d); got != c.want {
			t.Errorf("%s: NIPFeeOn(%d, %s) = %d, want %d", name, c.amount, c.d, got, c.want)
		}
	}
}

func TestVAT(t *testing.T) {
	s := nigeria(t)
	cases := map[string]struct{ base, want int64 }{
		"zero":                   {0, 0},
		"below half":             {1, 0},
		"above half":             {7, 1},
		"exact":                  {1000, 75},
		"half rounds up to even": {2500, 188},
		"half stays even":        {60, 4},
		"half 1.5 to 2":          {20, 2},
		"max int64":              {math.MaxInt64, 691752902764108186},
	}
	for name, c := range cases {
		if got := s.VAT(c.base); got != c.want {
			t.Errorf("%s: VAT(%d) = %d, want %d", name, c.base, got, c.want)
		}
	}
}

func TestStampDutyAndEMTL(t *testing.T) {
	s := nigeria(t)
	before := clock.Date{Y: 2025, M: 12, D: 31}
	after := clock.Date{Y: 2026, M: 1, D: 1}
	cases := map[string]struct {
		amount      int64
		d           clock.Date
		exempt      bool
		stamp, emtl int64
	}{
		"below threshold before switch": {999999, before, false, 0, 0},
		"at threshold before switch":    {1000000, before, false, 0, 5000},
		"below threshold after switch":  {999999, after, false, 0, 0},
		"at threshold after switch":     {1000000, after, false, 5000, 0},
		"exempt after switch":           {1000000, after, true, 0, 0},
		"exempt before switch":          {1000000, before, true, 0, 5000},
		"far future":                    {math.MaxInt64, clock.Date{Y: 2300, M: 2, D: 29}, false, 5000, 0},
		"before first rule":             {1000000, clock.Date{Y: 2019, M: 1, D: 1}, false, 0, 5000},
	}
	for name, c := range cases {
		if got := s.StampDuty(c.amount, c.d, c.exempt); got != c.stamp {
			t.Errorf("%s: StampDuty = %d, want %d", name, got, c.stamp)
		}
		if got := s.EMTL(c.amount, c.d); got != c.emtl {
			t.Errorf("%s: EMTL = %d, want %d", name, got, c.emtl)
		}
	}
}

func TestForTransfer(t *testing.T) {
	s := nigeria(t)
	draft := clock.Date{Y: 2026, M: 5, D: 1}
	cases := map[string]struct {
		amount int64
		d      clock.Date
		want   Transfer
		total  int64
	}{
		"one naira":            {100, draft, Transfer{}, 0},
		"top of free band":     {500000, draft, Transfer{}, 0},
		"above free band":      {500001, draft, Transfer{Fee: 1000, VAT: 75}, 1075},
		"top of middle band":   {5000000, draft, Transfer{Fee: 1000, VAT: 75, StampDuty: 5000}, 6075},
		"above middle band":    {5000001, draft, Transfer{Fee: 5000, VAT: 375, StampDuty: 5000}, 10375},
		"max amount":           {math.MaxInt64, clock.Date{Y: 2300, M: 2, D: 29}, Transfer{Fee: 5000, VAT: 375, StampDuty: 5000}, 10375},
		"day before draft":     {100, clock.Date{Y: 2026, M: 4, D: 30}, Transfer{Fee: 1000, VAT: 75}, 1075},
		"old bands in 2025":    {500001, clock.Date{Y: 2025, M: 12, D: 31}, Transfer{Fee: 2500, VAT: 188}, 2688},
		"old top band in 2025": {5000001, clock.Date{Y: 2025, M: 12, D: 31}, Transfer{Fee: 5000, VAT: 375}, 5375},
	}
	for name, c := range cases {
		got := s.ForTransfer(c.amount, c.d, false)
		if got != c.want || got.Total() != c.total {
			t.Errorf("%s: ForTransfer(%d, %s) = %+v total %d, want %+v total %d", name, c.amount, c.d, got, got.Total(), c.want, c.total)
		}
	}
	if s.VATRateBasisPoints() != 750 {
		t.Errorf("VAT rate = %d, want 750", s.VATRateBasisPoints())
	}
}

func TestNewRejectsBadRuleDates(t *testing.T) {
	pack, err := countrypack.Nigeria()
	if err != nil {
		t.Fatal(err)
	}
	nip := pack.Fees
	nip.NIP = append([]countrypack.NIPRule{{From: "soon"}}, nip.NIP...)
	stamp := pack.Fees
	stamp.StampDuty = append([]countrypack.Levy{{From: "2026-13-01"}}, stamp.StampDuty...)
	emtl := pack.Fees
	emtl.EMTL = append([]countrypack.Levy{{From: ""}}, emtl.EMTL...)
	cases := map[string]struct {
		fees countrypack.Fees
		want string
	}{
		"nip":   {nip, "parse nip rule date"},
		"stamp": {stamp, "parse stamp duty: parse rule date"},
		"emtl":  {emtl, "parse emtl: parse rule date"},
	}
	for name, c := range cases {
		if _, err := New(c.fees); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want it to contain %q", name, err, c.want)
		}
	}
}
