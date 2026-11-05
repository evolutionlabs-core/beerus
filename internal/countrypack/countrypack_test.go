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

func TestPhone(t *testing.T) {
	p, err := Nigeria()
	if err != nil {
		t.Fatal(err)
	}
	if p.Phone.CallingCode != "234" || p.Phone.TrunkPrefix != "0" || p.Phone.Source == "" {
		t.Errorf("phone: %+v", p.Phone)
	}
}
