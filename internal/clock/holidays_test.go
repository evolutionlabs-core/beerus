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

var declared = map[int][3]string{
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
	for y, want := range declared {
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
				t.Errorf("%d: %s computed %s, declared %s", y, []string{"Eid-el-Fitr", "Eid-el-Kabir", "Eid-el-Maulud"}[i], got[i], w)
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
