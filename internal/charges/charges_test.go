package charges

import (
	"math"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

func TestNIPFeeOn(t *testing.T) {
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
		"far future":         {500001, clock.Date{Y: 2300, M: 2, D: 29}, 2500},
	}
	for name, c := range cases {
		if got := NIPFeeOn(c.amount, c.d); got != c.want {
			t.Errorf("%s: NIPFeeOn(%d, %s) = %d, want %d", name, c.amount, c.d, got, c.want)
		}
	}
}

func TestVAT(t *testing.T) {
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
		if got := VAT(c.base); got != c.want {
			t.Errorf("%s: VAT(%d) = %d, want %d", name, c.base, got, c.want)
		}
	}
}

func TestStampDutyAndEMTL(t *testing.T) {
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
		if got := StampDuty(c.amount, c.d, c.exempt); got != c.stamp {
			t.Errorf("%s: StampDuty = %d, want %d", name, got, c.stamp)
		}
		if got := EMTL(c.amount, c.d); got != c.emtl {
			t.Errorf("%s: EMTL = %d, want %d", name, got, c.emtl)
		}
	}
}

func TestForTransferAndFigures(t *testing.T) {
	got := ForTransfer(1000000, clock.Date{Y: 2026, M: 1, D: 1}, false)
	if got != (Transfer{Fee: 2500, VAT: 188, StampDuty: 5000}) || got.Total() != 7688 {
		t.Errorf("ForTransfer = %+v, total %d", got, got.Total())
	}
	if VATRateBasisPoints() != 750 || SMSAlertFee() != 400 || SMSMonthlyCap() != 50000 || MaintenancePerMille() != 1 {
		t.Errorf("figures = %d %d %d %d", VATRateBasisPoints(), SMSAlertFee(), SMSMonthlyCap(), MaintenancePerMille())
	}
}
