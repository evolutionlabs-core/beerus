package clock

import (
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/countrypack"
)

func TestCalendar(t *testing.T) {
	pack, err := countrypack.Nigeria()
	if err != nil {
		t.Fatal(err)
	}
	fixed := pack.Fixed.Days
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
		{"christmas 2399", plain, "2399-12-25", true, false},
		{"christmas on a saturday", plain, "2027-12-25", true, false},
		{"no substitute monday", plain, "2027-12-27", false, true},
		{"democracy day", plain, "2026-06-12", false, true},
		{"boxing day on a saturday", plain, "2026-12-26", false, false},
		{"boxing day on a weekday", plain, "2027-12-26", false, false},
		{"boxing day 2028", plain, "2028-12-26", false, true},
		{"good friday", plain, "2026-04-03", false, true},
		{"eid el fitr", plain, "2026-03-20", false, true},
		{"leap day", plain, "2028-02-29", false, true},
		{"weekend", plain, "2026-11-07", false, false},
		{"month end on a Saturday", plain, "2026-10-31", false, false},
		{"month end on a Sunday", plain, "2027-02-28", false, false},
		{"month end on a Monday", plain, "2026-11-30", false, true},
		{"year end", plain, "2026-12-31", false, true},
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

func TestDateAddDays(t *testing.T) {
	for _, c := range []struct {
		name string
		from Date
		n    int
		want string
	}{
		{"the day before", Date{2026, 11, 14}, -1, "2026-11-13"},
		{"month start", Date{2026, 3, 1}, -1, "2026-02-28"},
		{"leap day", Date{2028, 3, 1}, -1, "2028-02-29"},
		{"year start", Date{2027, 1, 1}, -1, "2026-12-31"},
		{"90 days", Date{2026, 3, 1}, -90, "2025-12-01"},
		{"366 days across a leap day", Date{2028, 3, 1}, -366, "2027-03-01"},
		{"no days", Date{2026, 3, 1}, 0, "2026-03-01"},
		{"forward over year end", Date{2026, 12, 31}, 1, "2027-01-01"},
		{"far future", Date{2126, 3, 1}, -1, "2126-02-28"},
	} {
		if got := c.from.AddDays(c.n).String(); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}
