package charges

import (
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

type Band struct {
	UpTo int64
	Fee  int64
}

type FeeRule struct {
	From  clock.Date
	Bands []Band
}

var NIPFeeRules = []FeeRule{
	{From: clock.Date{Y: 2020, M: time.January, D: 1}, Bands: []Band{{UpTo: 500000, Fee: 1000}, {UpTo: 5000000, Fee: 2500}, {UpTo: -1, Fee: 5000}}},
}

var (
	VATRateBasisPoints  int64 = 750
	StampDutyFrom             = clock.Date{Y: 2026, M: time.January, D: 1}
	StampDutyThreshold  int64 = 1000000
	StampDutyAmount     int64 = 5000
	EMTLThreshold       int64 = 1000000
	EMTLAmount          int64 = 5000
	SMSAlertFee         int64 = 400
	SMSMonthlyCap       int64 = 50000
	MaintenancePerMille int64 = 1
)

func rule(d clock.Date) FeeRule {
	r := NIPFeeRules[0]
	for _, x := range NIPFeeRules {
		if !d.Before(x.From) {
			r = x
		}
	}
	return r
}

func NIPFeeOn(amount int64, d clock.Date) int64 {
	for _, b := range rule(d).Bands {
		if b.UpTo < 0 || amount <= b.UpTo {
			return b.Fee
		}
	}
	return 0
}

func NIPFee(amount int64) int64 {
	return NIPFeeOn(amount, clock.Date{Y: 2026, M: time.January, D: 1})
}

func VAT(base int64) int64 {
	num := base * VATRateBasisPoints
	q, r := num/10000, num%10000
	if r > 5000 || (r == 5000 && q%2 == 1) {
		q++
	}
	return q
}

func StampDuty(amount int64, d clock.Date, exempt bool) int64 {
	if exempt || amount < StampDutyThreshold || d.Before(StampDutyFrom) {
		return 0
	}
	return StampDutyAmount
}

func EMTL(amount int64, d clock.Date) int64 {
	if amount < EMTLThreshold || !d.Before(StampDutyFrom) {
		return 0
	}
	return EMTLAmount
}

type Transfer struct {
	Fee       int64
	VAT       int64
	StampDuty int64
}

func (t Transfer) Total() int64 {
	return t.Fee + t.VAT + t.StampDuty
}

func ForTransfer(amount int64, d clock.Date, stampExempt bool) Transfer {
	fee := NIPFeeOn(amount, d)
	return Transfer{Fee: fee, VAT: VAT(fee), StampDuty: StampDuty(amount, d, stampExempt)}
}
