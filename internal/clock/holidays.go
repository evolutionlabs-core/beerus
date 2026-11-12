package clock

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/countrypack"
)

type adjustments struct {
	add    []Date
	remove []Date
}

type Calendar struct {
	mu       sync.Mutex
	pack     *countrypack.Pack
	declared map[int]adjustments
	years    map[int]map[Date]bool
	add      map[Date]bool
	remove   map[Date]bool
}

var rules = mustRules()

func mustRules() *Calendar {
	c, err := newRules()
	if err != nil {
		panic(err)
	}
	return c
}

func newRules() (*Calendar, error) {
	p, err := countrypack.Nigeria()
	if err != nil {
		return nil, err
	}
	c := &Calendar{pack: p, declared: map[int]adjustments{}, add: map[Date]bool{}, remove: map[Date]bool{}}
	parse := func(year int, list []string) ([]Date, error) {
		var out []Date
		for _, s := range list {
			d, err := ParseDate(s)
			if err != nil {
				return nil, fmt.Errorf("country pack NG: declared %d: %q is not a date", year, s)
			}
			if d.Y != year {
				return nil, fmt.Errorf("country pack NG: declared %d: %s is in another year", year, s)
			}
			out = append(out, d)
		}
		return out, nil
	}
	for _, e := range p.Declared {
		var d adjustments
		if d.add, err = parse(e.Year, e.Add); err != nil {
			return nil, err
		}
		if d.remove, err = parse(e.Year, e.Remove); err != nil {
			return nil, err
		}
		c.declared[e.Year] = d
	}
	return c, nil
}

func NewCalendar(add, remove []Date) *Calendar {
	c := &Calendar{pack: rules.pack, declared: rules.declared, add: map[Date]bool{}, remove: map[Date]bool{}}
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
	s := c.buildYear(y)
	if d, ok := c.declared[y]; ok {
		for _, r := range d.remove {
			delete(s, r)
		}
		for _, a := range d.add {
			s[a] = true
		}
	}
	c.years[y] = s
	return s
}

func (c *Calendar) buildYear(y int) map[Date]bool {
	placed := map[Date]bool{}
	for _, ey := range []int{y - 1, y} {
		e := easter(ey)
		for _, h := range c.pack.Easter {
			placed[e.AddDays(h.Offset)] = true
		}
	}
	var bases []Date
	for _, by := range []int{y - 1, y} {
		for _, f := range c.pack.Fixed {
			bases = append(bases, Date{by, time.Month(f.Month), f.Day})
		}
	}
	bases = append(bases, c.islamic(y)...)
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

func (c *Calendar) islamic(y int) []Date {
	var out []Date
	approx := (y - 622) * 1030684 / 1000000
	for hy := approx - 1; hy <= approx+2; hy++ {
		for _, h := range c.pack.Islamic {
			first := FromHijri(Hijri{hy, h.Month, h.Day})
			for i := 0; i < h.Days; i++ {
				out = append(out, first.AddDays(i))
			}
		}
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
