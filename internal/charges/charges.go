package charges

import (
	"fmt"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/countrypack"
)

const (
	CategoryFee   = "fee"
	CategoryVAT   = "vat"
	CategoryStamp = "stamp"
	CategoryLevy  = "levy"
)

type nipRule struct {
	from  clock.Date
	bands []countrypack.Band
}

type levyRule struct {
	from      clock.Date
	threshold int64
	amount    int64
}

type Schedule struct {
	nip                 []nipRule
	stamp               []levyRule
	emtl                []levyRule
	vatRateBasisPoints  int64
	smsAlertFee         int64
	smsMonthlyCap       int64
	maintenancePerMille int64
}

func New(f countrypack.Fees) (*Schedule, error) {
	s := &Schedule{
		vatRateBasisPoints:  f.VATRateBasisPoints.Value,
		smsAlertFee:         f.SMSAlertFee.Value,
		smsMonthlyCap:       f.SMSMonthlyCap.Value,
		maintenancePerMille: f.MaintenancePerMille.Value,
	}
	for _, r := range f.NIP {
		from, err := clock.ParseDate(r.From)
		if err != nil {
			return nil, fmt.Errorf("parse nip rule date: %w", err)
		}
		s.nip = append(s.nip, nipRule{from: from, bands: r.Bands})
	}
	var err error
	if s.stamp, err = levyRules(f.StampDuty); err != nil {
		return nil, fmt.Errorf("parse stamp duty: %w", err)
	}
	if s.emtl, err = levyRules(f.EMTL); err != nil {
		return nil, fmt.Errorf("parse emtl: %w", err)
	}
	return s, nil
}

func levyRules(in []countrypack.Levy) ([]levyRule, error) {
	out := []levyRule{}
	for _, l := range in {
		from, err := clock.ParseDate(l.From)
		if err != nil {
			return nil, fmt.Errorf("parse rule date: %w", err)
		}
		out = append(out, levyRule{from: from, threshold: l.Threshold, amount: l.Amount})
	}
	return out, nil
}

func pick[T any](rules []T, d clock.Date, from func(T) clock.Date) T {
	r := rules[0]
	for _, x := range rules {
		if !d.Before(from(x)) {
			r = x
		}
	}
	return r
}

func levyOn(rules []levyRule, amount int64, d clock.Date) int64 {
	r := pick(rules, d, func(x levyRule) clock.Date { return x.from })
	if r.amount == 0 || amount < r.threshold {
		return 0
	}
	return r.amount
}

func (s *Schedule) VATRateBasisPoints() int64 {
	return s.vatRateBasisPoints
}

func (s *Schedule) SMSAlertFee() int64 {
	return s.smsAlertFee
}

func (s *Schedule) SMSMonthlyCap() int64 {
	return s.smsMonthlyCap
}

func (s *Schedule) MaintenancePerMille() int64 {
	return s.maintenancePerMille
}

func (s *Schedule) NIPFeeOn(amount int64, d clock.Date) int64 {
	bands := pick(s.nip, d, func(x nipRule) clock.Date { return x.from }).bands
	for i, b := range bands {
		if i == len(bands)-1 || amount <= b.UpTo {
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

func (s *Schedule) StampDuty(amount int64, d clock.Date, exempt bool) int64 {
	if exempt {
		return 0
	}
	return levyOn(s.stamp, amount, d)
}

func (s *Schedule) EMTL(amount int64, d clock.Date) int64 {
	return levyOn(s.emtl, amount, d)
}

type Transfer struct {
	Fee       int64
	VAT       int64
	StampDuty int64
}

func (t Transfer) Total() int64 {
	return t.Fee + t.VAT + t.StampDuty
}

func (s *Schedule) ForTransfer(amount int64, d clock.Date, stampExempt bool) Transfer {
	fee := s.NIPFeeOn(amount, d)
	return Transfer{Fee: fee, VAT: s.VAT(fee), StampDuty: s.StampDuty(amount, d, stampExempt)}
}
