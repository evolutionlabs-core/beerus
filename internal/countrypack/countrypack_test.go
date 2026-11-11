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

func TestFeesCheck(t *testing.T) {
	p, err := Nigeria()
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Fees.NIP) != 1 || len(p.Fees.NIP[0].Bands) != 3 || p.Fees.VATRateBasisPoints.Value != 750 {
		t.Errorf("fees = %+v", p.Fees)
	}
	bad := func(edit func(f *Fees)) error {
		f := p.Fees
		f.NIP = append([]NIPRule(nil), f.NIP...)
		f.EMTL = append([]Levy(nil), f.EMTL...)
		edit(&f)
		return f.check()
	}
	cases := map[string]struct {
		edit func(f *Fees)
		want string
	}{
		"no nip source":   {func(f *Fees) { f.NIP[0].Source = "" }, "fees.nip[0]: no source"},
		"last band bound": {func(f *Fees) { f.NIP[0].Bands = []Band{{UpTo: 5, Fee: 1}} }, "fees.nip[0]: the last band must have no up_to_minor, not 5"},
		"bands descend":   {func(f *Fees) { f.NIP[0].Bands = []Band{{UpTo: 9, Fee: 1}, {UpTo: 3, Fee: 2}, {Fee: 3}} }, "fees.nip[0]: band 1 up_to_minor 3 must be above 9"},
		"bad date":        {func(f *Fees) { f.EMTL[0].From = "soon" }, "fees.emtl[0] from \"soon\" must be a date like 2020-01-31"},
		"dates repeat":    {func(f *Fees) { f.EMTL[1].From = f.EMTL[0].From }, "fees.emtl[1] from 2020-01-01 must be later than 2020-01-01"},
		"vat too high":    {func(f *Fees) { f.VATRateBasisPoints.Value = 10001 }, "fees.vat_rate_basis_points must be from 0 to 10000, not 10001"},
		"no sms source":   {func(f *Fees) { f.SMSAlertFee.Source = "" }, "fees.sms_alert_fee_minor has no source"},
	}
	for name, c := range cases {
		if err := bad(c.edit); err == nil || err.Error() != c.want {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}
