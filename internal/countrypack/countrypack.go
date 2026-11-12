package countrypack

import (
	_ "embed"
	"fmt"
	"math"
	"time"

	"go.yaml.in/yaml/v3"
)

//go:embed ng.yaml
var ng []byte

type Fixed struct {
	Name  string `yaml:"name"`
	Month int    `yaml:"month"`
	Day   int    `yaml:"day"`
}

type Easter struct {
	Name   string `yaml:"name"`
	Offset int    `yaml:"offset"`
}

type Islamic struct {
	Name  string `yaml:"name"`
	Month int    `yaml:"month"`
	Day   int    `yaml:"day"`
	Days  int    `yaml:"days"`
}

type Declared struct {
	Year   int      `yaml:"year"`
	Add    []string `yaml:"add"`
	Remove []string `yaml:"remove"`
	Source string   `yaml:"source"`
}

type CodeRange struct {
	First int `yaml:"first"`
	Last  int `yaml:"last"`
}

type SimulatorCodes struct {
	NIP    CodeRange `yaml:"nip"`
	NUBAN  CodeRange `yaml:"nuban"`
	Source string    `yaml:"source"`
}

type Phone struct {
	CallingCode string `yaml:"calling_code"`
	TrunkPrefix string `yaml:"trunk_prefix"`
	Source      string `yaml:"source"`
}

type DepositInsurance struct {
	Limits      map[string]int64  `yaml:"limits_minor"`
	BankTypes   map[string]string `yaml:"bank_types"`
	Placeholder string            `yaml:"placeholder"`
	Source      string            `yaml:"source"`
}

func (d DepositInsurance) check() error {
	if d.Source == "" {
		return fmt.Errorf("deposit_insurance has no source")
	}
	if len(d.BankTypes) == 0 {
		return fmt.Errorf("deposit_insurance has no bank_types")
	}
	for typ, class := range d.BankTypes {
		if d.Limits[class] <= 0 {
			return fmt.Errorf("deposit_insurance: bank type %s maps to %q, which has no limit above zero", typ, class)
		}
	}
	return nil
}

func (d DepositInsurance) Limit(bankType string) (int64, error) {
	class, ok := d.BankTypes[bankType]
	if !ok {
		return 0, fmt.Errorf("deposit insurance has no limit for bank type %q", bankType)
	}
	return d.Limits[class], nil
}

type Band struct {
	UpTo int64 `yaml:"up_to_minor"`
	Fee  int64 `yaml:"fee_minor"`
}

type NIPRule struct {
	From   string `yaml:"from"`
	Bands  []Band `yaml:"bands"`
	Source string `yaml:"source"`
}

type Levy struct {
	From      string `yaml:"from"`
	Threshold int64  `yaml:"threshold_minor"`
	Amount    int64  `yaml:"amount_minor"`
	Source    string `yaml:"source"`
}

type Figure struct {
	Value  int64  `yaml:"value"`
	Source string `yaml:"source"`
}

type Fees struct {
	NIP                 []NIPRule `yaml:"nip"`
	StampDuty           []Levy    `yaml:"stamp_duty"`
	EMTL                []Levy    `yaml:"emtl"`
	VATRateBasisPoints  Figure    `yaml:"vat_rate_basis_points"`
	SMSAlertFee         Figure    `yaml:"sms_alert_fee_minor"`
	SMSMonthlyCap       Figure    `yaml:"sms_monthly_cap_minor"`
	MaintenancePerMille Figure    `yaml:"maintenance_per_mille"`
}

type Pack struct {
	Country  string     `yaml:"country"`
	Version  string     `yaml:"version"`
	Fixed    []Fixed    `yaml:"fixed"`
	Easter   []Easter   `yaml:"easter"`
	Islamic  []Islamic  `yaml:"islamic"`
	Declared []Declared `yaml:"declared"`

	SimulatorCodes   SimulatorCodes   `yaml:"simulator_codes"`
	Phone            Phone            `yaml:"phone"`
	DepositInsurance DepositInsurance `yaml:"deposit_insurance"`
	Fees             Fees             `yaml:"fees"`
}

func Nigeria() (*Pack, error) {
	var p Pack
	if err := yaml.Unmarshal(ng, &p); err != nil {
		return nil, fmt.Errorf("country pack NG: %w", err)
	}
	if p.Version == "" {
		return nil, fmt.Errorf("country pack NG: no version")
	}
	for _, d := range p.Declared {
		if d.Source == "" {
			return nil, fmt.Errorf("country pack NG: declared %d has no source", d.Year)
		}
	}
	if err := p.SimulatorCodes.check(); err != nil {
		return nil, fmt.Errorf("country pack NG: %w", err)
	}
	if p.Phone.CallingCode == "" || p.Phone.TrunkPrefix == "" || p.Phone.Source == "" {
		return nil, fmt.Errorf("country pack NG: phone needs calling_code, trunk_prefix and source")
	}
	if err := p.DepositInsurance.check(); err != nil {
		return nil, fmt.Errorf("country pack NG: %w", err)
	}
	if err := p.Fees.check(); err != nil {
		return nil, fmt.Errorf("country pack NG: %w", err)
	}
	return &p, nil
}

const maxFeeMinor = math.MaxInt64 / 4

func (f Fees) check() error {
	if len(f.NIP) == 0 {
		return fmt.Errorf("fees.nip needs at least one rule")
	}
	froms := make([]string, len(f.NIP))
	for i, r := range f.NIP {
		froms[i] = r.From
		if err := r.check(); err != nil {
			return fmt.Errorf("fees.nip[%d]: %w", i, err)
		}
	}
	if err := checkFroms("fees.nip", froms); err != nil {
		return err
	}
	for name, levies := range map[string][]Levy{"fees.stamp_duty": f.StampDuty, "fees.emtl": f.EMTL} {
		if len(levies) == 0 {
			return fmt.Errorf("%s needs at least one rule", name)
		}
		froms := make([]string, len(levies))
		for i, l := range levies {
			froms[i] = l.From
			if err := l.check(); err != nil {
				return fmt.Errorf("%s[%d]: %w", name, i, err)
			}
		}
		if err := checkFroms(name, froms); err != nil {
			return err
		}
	}
	if f.VATRateBasisPoints.Value < 0 || f.VATRateBasisPoints.Value > 10000 {
		return fmt.Errorf("fees.vat_rate_basis_points must be from 0 to 10000, not %d", f.VATRateBasisPoints.Value)
	}
	for name, fig := range map[string]Figure{
		"fees.vat_rate_basis_points": f.VATRateBasisPoints,
		"fees.sms_alert_fee_minor":   f.SMSAlertFee,
		"fees.sms_monthly_cap_minor": f.SMSMonthlyCap,
		"fees.maintenance_per_mille": f.MaintenancePerMille,
	} {
		if fig.Source == "" {
			return fmt.Errorf("%s has no source", name)
		}
		if fig.Value < 0 || fig.Value > maxFeeMinor {
			return fmt.Errorf("%s must be from 0 to %d, not %d", name, int64(maxFeeMinor), fig.Value)
		}
	}
	return nil
}

func checkFroms(name string, froms []string) error {
	for i, s := range froms {
		if _, err := time.Parse("2006-01-02", s); err != nil {
			return fmt.Errorf("%s[%d] from %q must be a date like 2020-01-31", name, i, s)
		}
		if i > 0 && s <= froms[i-1] {
			return fmt.Errorf("%s[%d] from %s must be later than %s", name, i, s, froms[i-1])
		}
	}
	return nil
}

func (r NIPRule) check() error {
	if r.Source == "" {
		return fmt.Errorf("no source")
	}
	if len(r.Bands) == 0 {
		return fmt.Errorf("needs at least one band")
	}
	last := len(r.Bands) - 1
	var prev int64
	for i, b := range r.Bands {
		if b.Fee < 0 || b.Fee > maxFeeMinor {
			return fmt.Errorf("band %d fee_minor must be from 0 to %d, not %d", i, int64(maxFeeMinor), b.Fee)
		}
		if i == last {
			if b.UpTo != 0 {
				return fmt.Errorf("the last band must have no up_to_minor, not %d", b.UpTo)
			}
			continue
		}
		if b.UpTo <= prev {
			return fmt.Errorf("band %d up_to_minor %d must be above %d", i, b.UpTo, prev)
		}
		prev = b.UpTo
	}
	return nil
}

func (l Levy) check() error {
	if l.Source == "" {
		return fmt.Errorf("no source")
	}
	if l.Threshold < 0 || l.Amount < 0 || l.Amount > maxFeeMinor {
		return fmt.Errorf("threshold_minor and amount_minor must not be negative, and amount_minor must be at most %d", int64(maxFeeMinor))
	}
	return nil
}

func (s SimulatorCodes) check() error {
	if s.Source == "" {
		return fmt.Errorf("simulator_codes has no source")
	}
	for name, r := range map[string]CodeRange{"nip": s.NIP, "nuban": s.NUBAN} {
		if r.First <= 0 || r.Last < r.First {
			return fmt.Errorf("simulator_codes.%s must have 0 < first <= last, not %d to %d", name, r.First, r.Last)
		}
	}
	return nil
}
