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

type Pack struct {
	Country  string     `yaml:"country"`
	Version  string     `yaml:"version"`
	Fixed    []Fixed    `yaml:"fixed"`
	Easter   []Easter   `yaml:"easter"`
	Islamic  []Islamic  `yaml:"islamic"`
	Declared []Declared `yaml:"declared"`

	SimulatorCodes SimulatorCodes `yaml:"simulator_codes"`
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
