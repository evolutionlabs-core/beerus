package clock

import "time"

var islamicHolidays = []string{
	"2023-04-21", "2023-04-24", "2023-06-28", "2023-06-29", "2023-09-28",
	"2024-04-10", "2024-04-11", "2024-06-17", "2024-06-18", "2024-09-16",
	"2025-03-31", "2025-04-01", "2025-06-06", "2025-06-09", "2025-09-04",
	"2026-03-19", "2026-03-20", "2026-05-27", "2026-05-28", "2026-08-25",
	"2027-03-10", "2027-03-11", "2027-05-17", "2027-05-18", "2027-08-16",
	"2028-02-28", "2028-02-29", "2028-05-05", "2028-05-08", "2028-08-03",
}

const (
	firstHolidayYear = 2023
	lastHolidayYear  = 2028
)

var holidays = buildHolidays()

func buildHolidays() map[Date]bool {
	out := map[Date]bool{}
	for _, s := range islamicHolidays {
		d, err := ParseDate(s)
		if err != nil {
			panic(err)
		}
		out[d] = true
	}
	for y := firstHolidayYear; y <= lastHolidayYear; y++ {
		for _, d := range fixedHolidays(y) {
			for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday || out[d] {
				d = d.AddDays(1)
			}
			out[d] = true
		}
		e := easter(y)
		out[e.AddDays(-2)] = true
		out[e.AddDays(1)] = true
	}
	return out
}

func fixedHolidays(y int) []Date {
	return []Date{
		{y, time.January, 1},
		{y, time.May, 1},
		{y, time.June, 12},
		{y, time.October, 1},
		{y, time.December, 25},
		{y, time.December, 26},
	}
}

func easter(y int) Date {
	a := y % 19
	b, c := y/100, y%100
	d, e := b/4, b%4
	f := (b + 8) / 25
	g := (b - f + 1) / 3
	h := (19*a + b - d - g + 15) % 30
	i, k := c/4, c%4
	l := (32 + 2*e + 2*i - h - k) % 7
	m := (a + 11*h + 22*l) / 451
	month := (h + l - 7*m + 114) / 31
	day := (h+l-7*m+114)%31 + 1
	return Date{y, time.Month(month), day}
}

func IsHoliday(d Date) bool {
	return holidays[d]
}

func IsBusinessDay(d Date) bool {
	w := d.Weekday()
	return w != time.Saturday && w != time.Sunday && !holidays[d]
}
