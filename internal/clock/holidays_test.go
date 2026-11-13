package clock

import (
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/countrypack"
)

func TestCalendar(t *testing.T) {
	fixed := []countrypack.Day{{Month: 1, Day: 1}, {Month: 5, Day: 1}, {Month: 10, Day: 1}, {Month: 12, Day: 25}}
	plain := NewCalendar(fixed, nil, nil)
	custom := NewCalendar(fixed, []Date{{2026, 6, 12}, {2026, 12, 24}}, []Date{{2026, 5, 1}, {2026, 12, 24}})
	cases := []struct {
		name     string
		cal      Calendar
		date     string
		holiday  bool
		business bool
	}{
		{"new year 2026", plain, "2026-01-01", true, false},
		{"workers day 2026", plain, "2026-05-01", true, false},
		{"independence day 2026", plain, "2026-10-01", true, false},
		{"christmas 2026", plain, "2026-12-25", true, false},
		{"new year 2399", plain, "2399-01-01", true, false},
		{"workers day 2399", plain, "2399-05-01", true, false},
		{"independence day 2399", plain, "2399-10-01", true, false},
		{"christmas 2399", plain, "2399-12-25", true, false},
		{"christmas on a saturday", plain, "2027-12-25", true, false},
		{"no substitute monday", plain, "2027-12-27", false, true},
		{"democracy day", plain, "2026-06-12", false, true},
		{"boxing day on a saturday", plain, "2026-12-26", false, false},
		{"boxing day on a weekday", plain, "2027-12-26", false, false},
		{"boxing day 2028", plain, "2028-12-26", false, true},
		{"good friday", plain, "2026-04-03", false, true},
		{"easter monday", plain, "2026-04-06", false, true},
		{"eid el fitr", plain, "2026-03-20", false, true},
		{"eid el kabir", plain, "2026-05-27", false, true},
		{"leap day", plain, "2028-02-29", false, true},
		{"weekend", plain, "2026-11-07", false, false},
		{"added day", custom, "2026-06-12", true, false},
		{"removed workers day", custom, "2026-05-01", false, true},
		{"workers day in another year", custom, "2029-05-01", true, false},
		{"removed beats added", custom, "2026-12-24", false, true},
	}
	for _, c := range cases {
		d, err := ParseDate(c.date)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := c.cal.IsHoliday(d); got != c.holiday {
			t.Errorf("%s: IsHoliday(%s) = %t, want %t", c.name, c.date, got, c.holiday)
		}
		if got := c.cal.IsBusinessDay(d); got != c.business {
			t.Errorf("%s: IsBusinessDay(%s) = %t, want %t", c.name, c.date, got, c.business)
		}
	}
	if (Calendar{}).IsHoliday(Date{2026, 1, 1}) {
		t.Error("the zero calendar has no holidays")
	}
}
