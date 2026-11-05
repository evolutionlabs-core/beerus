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

type Phone struct {
	CallingCode string `yaml:"calling_code"`
	TrunkPrefix string `yaml:"trunk_prefix"`
	Source      string `yaml:"source"`
}

type Pack struct {
	Country  string     `yaml:"country"`
	Version  string     `yaml:"version"`
	Fixed    []Fixed    `yaml:"fixed"`
	Easter   []Easter   `yaml:"easter"`
	Islamic  []Islamic  `yaml:"islamic"`
	Declared []Declared `yaml:"declared"`
	Phone    Phone      `yaml:"phone"`
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
	if p.Phone.CallingCode == "" || p.Phone.TrunkPrefix == "" || p.Phone.Source == "" {
		return nil, fmt.Errorf("country pack NG: phone needs calling_code, trunk_prefix and source")
	}
	return &p, nil
}
