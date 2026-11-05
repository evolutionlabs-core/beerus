package countrypack

import (
	_ "embed"
	"fmt"
	"strings"

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

type OutsideBank struct {
	Name    string `yaml:"name"`
	NIPCode string `yaml:"nip_code"`
	Type    string `yaml:"type"`
}

type OutsideBanks struct {
	Source string        `yaml:"source"`
	Banks  []OutsideBank `yaml:"banks"`
}

type Phone struct {
	CallingCode string `yaml:"calling_code"`
	TrunkPrefix string `yaml:"trunk_prefix"`
	Source      string `yaml:"source"`
}

type Pack struct {
	Country  string       `yaml:"country"`
	Version  string       `yaml:"version"`
	Fixed    []Fixed      `yaml:"fixed"`
	Easter   []Easter     `yaml:"easter"`
	Islamic  []Islamic    `yaml:"islamic"`
	Declared []Declared   `yaml:"declared"`
	Outside  OutsideBanks `yaml:"outside_banks"`
	Phone    Phone        `yaml:"phone"`
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
	if err := p.Outside.check(); err != nil {
		return nil, fmt.Errorf("country pack NG: %w", err)
	}
	return &p, nil
}

func (o OutsideBanks) check() error {
	if o.Source == "" {
		return fmt.Errorf("outside_banks has no source")
	}
	if len(o.Banks) == 0 {
		return fmt.Errorf("outside_banks has no banks")
	}
	codes := map[string]bool{}
	for i, b := range o.Banks {
		if b.Name == "" || b.Type == "" {
			return fmt.Errorf("outside bank %d needs a name and a type", i+1)
		}
		if len(b.NIPCode) != 6 || strings.Trim(b.NIPCode, "0123456789") != "" {
			return fmt.Errorf("outside bank %s: nip_code %q must be 6 digits", b.Name, b.NIPCode)
		}
		if codes[b.NIPCode] {
			return fmt.Errorf("outside bank %s: nip_code %s is used twice", b.Name, b.NIPCode)
		}
		codes[b.NIPCode] = true
	}
	return nil
}
