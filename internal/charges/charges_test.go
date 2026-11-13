package charges

import (
	"math"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/countrypack"
)

func nigeria(t *testing.T) *Schedule {
	t.Helper()
	pack, err := countrypack.Nigeria()
	if err != nil {
		t.Fatal(err)
	}
	return New(pack.Fees)
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

func TestForTransfer(t *testing.T) {
	s := nigeria(t)
	cases := map[string]struct {
		amount int64
		exempt bool
		want   Transfer
		total  int64
	}{
		"one naira":             {100, false, Transfer{}, 0},
		"top of free band":      {500000, false, Transfer{}, 0},
		"above free band":       {500001, false, Transfer{Fee: 1000, VAT: 75}, 1075},
		"below stamp threshold": {999999, false, Transfer{Fee: 1000, VAT: 75}, 1075},
		"at stamp threshold":    {1000000, false, Transfer{Fee: 1000, VAT: 75, StampDuty: 5000}, 6075},
		"top of middle band":    {5000000, false, Transfer{Fee: 1000, VAT: 75, StampDuty: 5000}, 6075},
		"above middle band":     {5000001, false, Transfer{Fee: 5000, VAT: 375, StampDuty: 5000}, 10375},
		"stamp exempt":          {10000000, true, Transfer{Fee: 5000, VAT: 375}, 5375},
		"max amount":            {math.MaxInt64, false, Transfer{Fee: 5000, VAT: 375, StampDuty: 5000}, 10375},
	}
	for name, c := range cases {
		got := s.ForTransfer(c.amount, c.exempt)
		if got != c.want || got.Total() != c.total {
			t.Errorf("%s: ForTransfer(%d, %t) = %+v total %d, want %+v total %d", name, c.amount, c.exempt, got, got.Total(), c.want, c.total)
		}
	}
	if s.VATRateBasisPoints() != 750 {
		t.Errorf("VAT rate = %d, want 750", s.VATRateBasisPoints())
	}
}
