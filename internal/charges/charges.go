package charges

import "github.com/evolutionlabs-core/beerus/internal/countrypack"

const (
	CategoryFee   = "fee"
	CategoryVAT   = "vat"
	CategoryStamp = "stamp"
)

type Schedule struct {
	bands              []countrypack.Band
	stampThreshold     int64
	stampAmount        int64
	vatRateBasisPoints int64
}

func New(f countrypack.Fees) *Schedule {
	return &Schedule{
		bands:              f.NIP.Bands,
		stampThreshold:     f.StampDuty.Threshold,
		stampAmount:        f.StampDuty.Amount,
		vatRateBasisPoints: f.VATRateBasisPoints.Value,
	}
}

func (s *Schedule) VATRateBasisPoints() int64 {
	return s.vatRateBasisPoints
}

func (s *Schedule) NIPFeeOn(amount int64) int64 {
	for i, b := range s.bands {
		if i == len(s.bands)-1 || amount <= b.UpTo {
			return b.Fee
		}
	}
	return 0
}

func (s *Schedule) VAT(base int64) int64 {
	rate := s.vatRateBasisPoints
	q, r := base/10000, base%10000
	res := q*rate + r*rate/10000
	rem := r * rate % 10000
	if rem > 5000 || (rem == 5000 && res%2 == 1) {
		res++
	}
	return res
}

func (s *Schedule) StampDuty(amount int64, exempt bool) int64 {
	if exempt || amount < s.stampThreshold {
		return 0
	}
	return s.stampAmount
}

type Transfer struct {
	Fee       int64
	VAT       int64
	StampDuty int64
}

func (t Transfer) Total() int64 {
	return t.Fee + t.VAT + t.StampDuty
}

func (s *Schedule) ForTransfer(amount int64, stampExempt bool) Transfer {
	fee := s.NIPFeeOn(amount)
	return Transfer{Fee: fee, VAT: s.VAT(fee), StampDuty: s.StampDuty(amount, stampExempt)}
}
