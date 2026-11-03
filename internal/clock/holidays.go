package clock

import (
	"sort"
	"sync"
	"time"
)

type Calendar struct {
	mu     sync.Mutex
	years  map[int]map[Date]bool
	add    map[Date]bool
	remove map[Date]bool
}

var rules = &Calendar{}

func NewCalendar(add, remove []Date) *Calendar {
	c := &Calendar{add: map[Date]bool{}, remove: map[Date]bool{}}
	for _, d := range add {
		c.add[d] = true
	}
	for _, d := range remove {
		c.remove[d] = true
	}
	return c
}

func (c *Calendar) IsHoliday(d Date) bool {
	if c == nil {
		c = rules
	}
	if c.remove[d] {
		return false
	}
	if c.add[d] {
		return true
	}
	return c.year(d.Y)[d]
}

func (c *Calendar) IsBusinessDay(d Date) bool {
	w := d.Weekday()
	return w != time.Saturday && w != time.Sunday && !c.IsHoliday(d)
}

func (c *Calendar) year(y int) map[Date]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.years[y]; ok {
		return s
	}
	if c.years == nil {
		c.years = map[int]map[Date]bool{}
	}
	s := buildYear(y)
	c.years[y] = s
	return s
}

func buildYear(y int) map[Date]bool {
	placed := map[Date]bool{}
	for _, ey := range []int{y - 1, y} {
		e := easter(ey)
		placed[e.AddDays(-2)] = true
		placed[e.AddDays(1)] = true
	}
	var bases []Date
	for _, by := range []int{y - 1, y} {
		bases = append(bases, fixedHolidays(by)...)
	}
	bases = append(bases, islamicHolidays(y)...)
	sort.SliceStable(bases, func(i, j int) bool { return bases[i].Before(bases[j]) })
	start := Date{y - 1, time.November, 1}
	for _, d := range bases {
		if d.Before(start) {
			continue
		}
		for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday || placed[d] {
			d = d.AddDays(1)
		}
		placed[d] = true
	}
	out := map[Date]bool{}
	for d := range placed {
		if d.Y == y {
			out[d] = true
		}
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

func islamicHolidays(y int) []Date {
	var out []Date
	approx := (y - 622) * 1030684 / 1000000
	for hy := approx - 1; hy <= approx+2; hy++ {
		fitr := FromHijri(Hijri{hy, 10, 1})
		kabir := FromHijri(Hijri{hy, 12, 10})
		out = append(out, fitr, fitr.AddDays(1), kabir, kabir.AddDays(1), FromHijri(Hijri{hy, 3, 12}))
	}
	return out
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
	return rules.IsHoliday(d)
}

func IsBusinessDay(d Date) bool {
	return rules.IsBusinessDay(d)
}
