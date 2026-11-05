package countrypack

import "testing"

func TestNigeria(t *testing.T) {
	p, err := Nigeria()
	if err != nil {
		t.Fatal(err)
	}
	if p.Country != "NG" || len(p.Fixed) != 6 || len(p.Easter) != 2 || len(p.Islamic) != 3 {
		t.Errorf("unexpected pack: %+v", p)
	}
	if c := p.SimulatorCodes; c.Source == "" || c.NIP != (CodeRange{First: 999001, Last: 999999}) || c.NUBAN != (CodeRange{First: 901, Last: 999}) {
		t.Errorf("simulator codes = %+v", c)
	}
	for _, d := range p.Declared {
		if d.Source == "" || len(d.Add)+len(d.Remove) == 0 {
			t.Errorf("declared %d has no source or no dates", d.Year)
		}
	}
}

func TestSimulatorCodesCheck(t *testing.T) {
	ok := CodeRange{First: 1, Last: 2}
	cases := map[string]struct {
		in   SimulatorCodes
		want string
	}{
		"no source":   {SimulatorCodes{NIP: ok, NUBAN: ok}, "simulator_codes has no source"},
		"zero first":  {SimulatorCodes{NIP: CodeRange{Last: 5}, NUBAN: ok, Source: "x"}, "simulator_codes.nip must have 0 < first <= last, not 0 to 5"},
		"last before": {SimulatorCodes{NIP: ok, NUBAN: CodeRange{First: 9, Last: 3}, Source: "x"}, "simulator_codes.nuban must have 0 < first <= last, not 9 to 3"},
	}
	for name, c := range cases {
		err := c.in.check()
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	if err := (SimulatorCodes{NIP: ok, NUBAN: ok, Source: "x"}).check(); err != nil {
		t.Errorf("valid codes: %v", err)
	}
}
