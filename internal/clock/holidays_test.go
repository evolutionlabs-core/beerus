package clock

import (
	"testing"
	"time"
)

func mustDate(t *testing.T, s string) Date {
	t.Helper()
	d, err := ParseDate(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestHolidays(t *testing.T) {
	cases := map[string]bool{
		"2023-01-01": false,
		"2023-01-02": true,
		"2023-04-07": true,
		"2023-04-10": true,
		"2023-10-02": true,
		"2024-03-29": true,
		"2024-04-01": true,
		"2025-04-18": true,
		"2026-10-01": true,
		"2026-12-25": true,
		"2026-12-26": false,
		"2026-12-28": true,
		"2027-03-26": true,
		"2027-12-27": true,
		"2027-12-28": true,
		"2028-01-03": true,
		"2028-04-14": true,
		"2028-10-02": true,
		"2026-10-02": false,
		"2026-11-03": false,
	}
	for s, want := range cases {
		if got := IsHoliday(mustDate(t, s)); got != want {
			t.Errorf("%s: holiday %v, want %v", s, got, want)
		}
	}
}

func TestEaster(t *testing.T) {
	for y, want := range map[int]string{2023: "2023-04-09", 2024: "2024-03-31", 2025: "2025-04-20", 2026: "2026-04-05", 2027: "2027-03-28", 2028: "2028-04-16"} {
		if got := easter(y).String(); got != want {
			t.Errorf("%d: %s, want %s", y, got, want)
		}
	}
}

func TestIsBusinessDay(t *testing.T) {
	cases := map[string]bool{
		"2026-10-01": false,
		"2026-10-02": true,
		"2026-10-03": false,
		"2026-10-04": false,
		"2026-10-05": true,
		"2025-12-25": false,
	}
	for s, want := range cases {
		if got := IsBusinessDay(mustDate(t, s)); got != want {
			t.Errorf("%s: business day %v, want %v", s, got, want)
		}
	}
}

var festival = map[int][3]string{
	2023: {"2023-04-21", "2023-06-28", "2023-09-27"},
	2024: {"2024-04-10", "2024-06-17", "2024-09-16"},
	2025: {"2025-03-31", "2025-06-06", "2025-09-04"},
	2026: {"2026-03-19", "2026-05-27", "2026-08-25"},
}

func hijriIn(y, month, day int) Date {
	for hy := (y-622)*1030684/1000000 - 2; hy <= (y-622)*1030684/1000000+2; hy++ {
		if d := FromHijri(Hijri{hy, month, day}); d.Y == y {
			return d
		}
	}
	panic("no such date")
}

func TestIslamicDatesAgainstDeclared(t *testing.T) {
	exact, off, far := 0, 0, 0
	for y, want := range festival {
		got := [3]Date{hijriIn(y, 10, 1), hijriIn(y, 12, 10), hijriIn(y, 3, 12)}
		for i, w := range want {
			diff := DaysBetween(mustDate(t, w), got[i])
			switch {
			case diff == 0:
				exact++
			case diff == 1 || diff == -1:
				off++
			default:
				far++
				t.Errorf("%d: %s computed %s, festival %s", y, []string{"Eid-el-Fitr", "Eid-el-Kabir", "Eid-el-Maulud"}[i], got[i], w)
			}
		}
	}
	t.Logf("exact %d, off by one day %d, further %d", exact, off, far)
	if exact < 5 {
		t.Errorf("only %d of 12 match exactly", exact)
	}
}

func TestHijriRoundTrip(t *testing.T) {
	if got := FromHijri(Hijri{1447, 1, 1}); got != mustDate(t, "2025-06-27") {
		t.Errorf("1 Muharram 1447 is %s", got)
	}
	d := mustDate(t, "1990-01-01")
	for i := 0; i < 50000; i++ {
		if got := FromHijri(ToHijri(d)); got != d {
			t.Fatalf("%s came back as %s", d, got)
		}
		d = d.AddDays(1)
	}
}

func TestFarYears(t *testing.T) {
	count := func(y int) (n int) {
		for d := (Date{y, time.January, 1}); d.Y == y; d = d.AddDays(1) {
			if IsHoliday(d) {
				n++
				if wd := d.Weekday(); wd == time.Saturday || wd == time.Sunday {
					t.Errorf("%s is a holiday on a weekend", d)
				}
			}
		}
		return n
	}
	if n := count(2035); n != 13 {
		t.Errorf("2035 has %d holidays, want 13", n)
	}
	for y := 1950; y <= 2200; y += 7 {
		if n := count(y); n < 12 || n > 18 {
			t.Errorf("%d has %d holidays", y, n)
		}
	}
}

func TestCalendarOverrides(t *testing.T) {
	base := mustDate(t, "2026-05-27")
	moved := mustDate(t, "2026-05-29")
	if !IsHoliday(base) || IsHoliday(moved) {
		t.Fatal("rules should hold 2026-05-27 and not 2026-05-29")
	}
	c := NewCalendar([]Date{moved}, []Date{base})
	if c.IsHoliday(base) || !c.IsHoliday(moved) {
		t.Error("overrides were not applied")
	}
	if c.IsBusinessDay(moved) || !c.IsBusinessDay(base) {
		t.Error("business days ignore the overrides")
	}
	if !IsHoliday(base) {
		t.Error("overrides leaked into the default calendar")
	}
	var none *Calendar
	if !none.IsHoliday(base) {
		t.Error("a nil calendar should follow the rules")
	}
}

func holidaysOf(c *Calendar, y int, from, to time.Month) []string {
	var out []string
	for d := (Date{y, from, 1}); d.Y == y && d.M <= to; d = d.AddDays(1) {
		if c.IsHoliday(d) {
			out = append(out, d.String())
		}
	}
	return out
}

func sameList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestHolidayYears(t *testing.T) {
	cases := map[int][]string{
		2022: {"2022-01-03", "2022-04-15", "2022-04-18", "2022-05-02", "2022-05-03", "2022-06-13", "2022-07-11", "2022-07-12", "2022-10-03", "2022-10-10", "2022-12-26", "2022-12-27"},
		2029: {"2029-01-01", "2029-02-15", "2029-02-16", "2029-03-30", "2029-04-02", "2029-04-24", "2029-04-25", "2029-05-01", "2029-06-12", "2029-07-24", "2029-10-01", "2029-12-25", "2029-12-26"},
		2040: {"2040-01-02", "2040-03-26", "2040-03-30", "2040-04-02", "2040-05-01", "2040-06-12", "2040-10-01", "2040-10-08", "2040-10-09", "2040-12-17", "2040-12-18", "2040-12-25", "2040-12-26"},
	}
	for y, want := range cases {
		if got := holidaysOf(NewCalendar(nil, nil), y, time.January, time.December); !sameList(got, want) {
			t.Errorf("%d: got %v, want %v", y, got, want)
		}
	}
}

func TestWeekendQueue(t *testing.T) {
	cases := []struct {
		year       int
		from, to   time.Month
		want       []string
		collisions string
	}{
		{2021, time.December, time.December, []string{"2021-12-27", "2021-12-28"}, "Christmas Saturday, Boxing Day Sunday"},
		{2022, time.December, time.December, []string{"2022-12-26", "2022-12-27"}, "Christmas Sunday, Boxing Day Monday"},
		{2033, time.January, time.January, []string{"2033-01-03", "2033-01-04", "2033-01-05"}, "New Year Saturday with Eid on Monday and Tuesday"},
		{2039, time.December, time.December, []string{"2039-12-26", "2039-12-27", "2039-12-28", "2039-12-29"}, "Christmas Sunday with both Eid days behind Boxing Day"},
		{2170, time.March, time.April, []string{"2170-03-30", "2170-04-02", "2170-04-03"}, "Eid on Good Friday queues behind Easter Monday"},
	}
	for _, c := range cases {
		if got := holidaysOf(NewCalendar(nil, nil), c.year, c.from, c.to); !sameList(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.collisions, got, c.want)
		}
	}
}

func TestDeclaredAdjustments(t *testing.T) {
	p, err := newRules()
	if err != nil {
		t.Fatal(err)
	}
	if len(p.declared) < 5 {
		t.Fatalf("only %d years of declared dates", len(p.declared))
	}
	c := NewCalendar(nil, nil)
	for y, adj := range p.declared {
		for _, d := range adj.add {
			if !c.IsHoliday(d) {
				t.Errorf("%d: declared %s is not a holiday", y, d)
			}
		}
		for _, d := range adj.remove {
			if c.IsHoliday(d) {
				t.Errorf("%d: %s should not be a holiday", y, d)
			}
		}
	}
	if got, want := holidaysOf(c, 2023, time.April, time.April), []string{"2023-04-07", "2023-04-10", "2023-04-21", "2023-04-24"}; !sameList(got, want) {
		t.Errorf("April 2023: got %v, want %v", got, want)
	}
	if got, want := holidaysOf(c, 2025, time.June, time.June), []string{"2025-06-06", "2025-06-09", "2025-06-12"}; !sameList(got, want) {
		t.Errorf("June 2025: got %v, want %v", got, want)
	}
}

func TestCalendarVersion(t *testing.T) {
	if CalendarVersion() == "" {
		t.Error("the calendar has no version")
	}
}
