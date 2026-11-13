package countrypack

import "testing"

func TestNigeria(t *testing.T) {
	p, err := Nigeria()
	if err != nil {
		t.Fatal(err)
	}
	if p.Country != "NG" || len(p.Fixed.Days) != 4 || p.Fixed.Source == "" {
		t.Errorf("unexpected pack: %+v", p)
	}
	if p.BankCodes.Source == "" || len(p.BankCodes.Categories) != 2 {
		t.Errorf("bank codes = %+v", p.BankCodes)
	}
}

func TestBankCodesCheck(t *testing.T) {
	types := map[string]string{"commercial": "dmb"}
	good := CodeTier{First: 1, Last: 999, NUBANDigits: 3, NIPPrefix: "000", NIPDigits: 3}
	next := CodeTier{First: 1000, Last: 89999, NUBANDigits: 6, NIPDigits: 6}
	cases := []struct {
		name string
		in   BankCodes
		want string
	}{
		{"no source", BankCodes{Categories: map[string][]CodeTier{"dmb": {good}}}, "bank_codes has no source"},
		{"no tiers", BankCodes{Categories: map[string][]CodeTier{}, Source: "x"}, "bank_codes has no tiers for dmb"},
		{"zero first", BankCodes{Categories: map[string][]CodeTier{"dmb": {{Last: 5, NUBANDigits: 3, NIPPrefix: "000", NIPDigits: 3}}}, Source: "x"}, "bank_codes.dmb[0] must have 1 <= first <= last, not 0 to 5"},
		{"overlap", BankCodes{Categories: map[string][]CodeTier{"dmb": {good, {First: 500, Last: 9999, NUBANDigits: 6, NIPDigits: 6}}}, Source: "x"}, "bank_codes.dmb[1] starts at 500, but the tier before it ends at 999"},
		{"descending", BankCodes{Categories: map[string][]CodeTier{"dmb": {next, good}}, Source: "x"}, "bank_codes.dmb[1] starts at 1, but the tier before it ends at 89999"},
		{"short nip", BankCodes{Categories: map[string][]CodeTier{"dmb": {{First: 1, Last: 9, NUBANDigits: 3, NIPDigits: 3}}}, Source: "x"}, "bank_codes.dmb[0] must make NIP codes of 6 digits"},
		{"bad nuban length", BankCodes{Categories: map[string][]CodeTier{"dmb": {{First: 1, Last: 9, NUBANDigits: 4, NIPPrefix: "000", NIPDigits: 3}}}, Source: "x"}, "bank_codes.dmb[0] must make NUBAN codes of 3, 5 or 6 digits"},
		{"letters in a prefix", BankCodes{Categories: map[string][]CodeTier{"dmb": {{First: 1, Last: 9, NUBANDigits: 3, NIPPrefix: "0x0", NIPDigits: 3}}}, Source: "x"}, "bank_codes.dmb[0] prefixes must be digits"},
		{"last too big", BankCodes{Categories: map[string][]CodeTier{"dmb": {{First: 1, Last: 1000, NUBANDigits: 3, NIPPrefix: "000", NIPDigits: 3}}}, Source: "x"}, "bank_codes.dmb[0] ends at 1000, which does not fit its digits"},
	}
	for _, c := range cases {
		err := c.in.check(types)
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
	if err := (BankCodes{Categories: map[string][]CodeTier{"dmb": {good, next}}, Source: "x"}).check(types); err != nil {
		t.Errorf("valid codes: %v", err)
	}
}

func TestBankCodeFormats(t *testing.T) {
	p, err := Nigeria()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		bankType, category, nip, nuban string
		n, tier                        int
	}{
		{"commercial", "deposit_money_bank", "000001", "001", 1, 0},
		{"digital", "deposit_money_bank", "001000", "001000", 1000, 1},
		{"microfinance", "microfinance_bank", "090001", "50001", 1, 0},
		{"microfinance", "microfinance_bank", "091000", "51000", 1000, 1},
	} {
		category, tiers := p.CodeTiers(c.bankType)
		if category != c.category || tiers[c.tier].NIP(c.n) != c.nip || tiers[c.tier].NUBAN(c.n) != c.nuban {
			t.Errorf("%s %d = %s %s %s", c.bankType, c.n, category, tiers[c.tier].NIP(c.n), tiers[c.tier].NUBAN(c.n))
		}
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

func TestFeesCheck(t *testing.T) {
	p, err := Nigeria()
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Fees.NIP.Bands) != 3 || p.Fees.StampDuty.Amount != 5000 || p.Fees.VATRateBasisPoints.Value != 750 {
		t.Errorf("fees = %+v", p.Fees)
	}
	bad := func(edit func(f *Fees)) error {
		f := p.Fees
		f.NIP.Bands = append([]Band(nil), f.NIP.Bands...)
		edit(&f)
		return f.check()
	}
	cases := map[string]struct {
		edit func(f *Fees)
		want string
	}{
		"no nip source":   {func(f *Fees) { f.NIP.Source = "" }, "fees.nip: no source"},
		"last band bound": {func(f *Fees) { f.NIP.Bands = []Band{{UpTo: 5, Fee: 1}} }, "fees.nip: the last band must have no up_to_minor, not 5"},
		"bands descend":   {func(f *Fees) { f.NIP.Bands = []Band{{UpTo: 9, Fee: 1}, {UpTo: 3, Fee: 2}, {Fee: 3}} }, "fees.nip: band 1 up_to_minor 3 must be above 9"},
		"no stamp source": {func(f *Fees) { f.StampDuty.Source = "" }, "fees.stamp_duty: no source"},
		"no nip bands":    {func(f *Fees) { f.NIP.Bands = nil }, "fees.nip: needs at least one band"},
		"negative stamp":  {func(f *Fees) { f.StampDuty.Amount = -1 }, "fees.stamp_duty: threshold_minor and amount_minor must not be negative, and amount_minor must be at most 2305843009213693951"},
		"vat too high":    {func(f *Fees) { f.VATRateBasisPoints.Value = 10001 }, "fees.vat_rate_basis_points must be from 0 to 10000, not 10001"},
		"no vat source":   {func(f *Fees) { f.VATRateBasisPoints.Source = "" }, "fees.vat_rate_basis_points has no source"},
	}
	for name, c := range cases {
		if err := bad(c.edit); err == nil || err.Error() != c.want {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}
