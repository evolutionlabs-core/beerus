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

func TestPhone(t *testing.T) {
	p, err := Nigeria()
	if err != nil {
		t.Fatal(err)
	}
	if p.Phone.CallingCode != "234" || p.Phone.TrunkPrefix != "0" || p.Phone.Source == "" {
		t.Errorf("phone: %+v", p.Phone)
	}
}

func TestDepositInsurance(t *testing.T) {
	p, err := Nigeria()
	if err != nil {
		t.Fatal(err)
	}
	for typ, want := range map[string]int64{"commercial": 500000000, "digital": 500000000, "microfinance": 200000000} {
		got, err := p.DepositInsurance.Limit(typ)
		if err != nil || got != want {
			t.Fatalf("%s: %d %v, want %d", typ, got, err, want)
		}
	}
	if _, err := p.DepositInsurance.Limit("savings"); err == nil || err.Error() != `deposit insurance has no limit for bank type "savings"` {
		t.Fatalf("unknown type: %v", err)
	}
	bad := DepositInsurance{Limits: map[string]int64{"dmb": 0}, BankTypes: map[string]string{"commercial": "dmb"}, Source: "x"}
	if err := bad.check(); err == nil || err.Error() != `deposit_insurance: bank type commercial maps to "dmb", which has no limit above zero` {
		t.Fatalf("zero limit: %v", err)
	}
	if err := (DepositInsurance{BankTypes: map[string]string{"commercial": "dmb"}}).check(); err == nil || err.Error() != "deposit_insurance has no source" {
		t.Fatalf("no source: %v", err)
	}
}
