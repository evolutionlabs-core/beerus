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
	for _, d := range p.Declared {
		if d.Source == "" || len(d.Add)+len(d.Remove) == 0 {
			t.Errorf("declared %d has no source or no dates", d.Year)
		}
	}
}

func TestOutsideBanks(t *testing.T) {
	p, err := Nigeria()
	if err != nil {
		t.Fatal(err)
	}
	want := []OutsideBank{
		{Name: "Bank A", NIPCode: "999001", Type: "commercial"},
		{Name: "Bank B", NIPCode: "999002", Type: "digital"},
		{Name: "Bank C", NIPCode: "999003", Type: "commercial"},
		{Name: "Bank D Microfinance", NIPCode: "999004", Type: "microfinance"},
	}
	if len(p.Outside.Banks) != len(want) {
		t.Fatalf("got %d outside banks, want %d", len(p.Outside.Banks), len(want))
	}
	for i, b := range want {
		if p.Outside.Banks[i] != b {
			t.Errorf("outside bank %d = %+v, want %+v", i, p.Outside.Banks[i], b)
		}
	}
}

func TestOutsideBanksCheck(t *testing.T) {
	ok := OutsideBank{Name: "Bank C", NIPCode: "999003", Type: "commercial"}
	cases := []struct {
		name string
		in   OutsideBanks
		want string
	}{
		{"no source", OutsideBanks{Banks: []OutsideBank{ok}}, "outside_banks has no source"},
		{"no banks", OutsideBanks{Source: "s"}, "outside_banks has no banks"},
		{"no name", OutsideBanks{Source: "s", Banks: []OutsideBank{{NIPCode: "999003", Type: "commercial"}}}, "outside bank 1 needs a name and a type"},
		{"short code", OutsideBanks{Source: "s", Banks: []OutsideBank{{Name: "X", NIPCode: "901", Type: "commercial"}}}, `outside bank X: nip_code "901" must be 6 digits`},
		{"letters", OutsideBanks{Source: "s", Banks: []OutsideBank{{Name: "X", NIPCode: "99900a", Type: "commercial"}}}, `outside bank X: nip_code "99900a" must be 6 digits`},
		{"twice", OutsideBanks{Source: "s", Banks: []OutsideBank{ok, ok}}, "outside bank Bank C: nip_code 999003 is used twice"},
	}
	for _, c := range cases {
		err := c.in.check()
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: got %v, want %q", c.name, err, c.want)
		}
	}
	if err := (OutsideBanks{Source: "s", Banks: []OutsideBank{ok}}).check(); err != nil {
		t.Errorf("valid list: %v", err)
	}
}
