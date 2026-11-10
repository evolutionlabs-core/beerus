package countrypack

import (
	_ "embed"
	"fmt"

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
	return &p, nil
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
