package clock

import (
	"time"

	"github.com/evolutionlabs-core/beerus/internal/countrypack"
)

type Calendar struct {
	fixed  []countrypack.Day
	add    map[Date]bool
	remove map[Date]bool
}

func NewCalendar(fixed []countrypack.Day, add, remove []Date) Calendar {
	c := Calendar{fixed: fixed, add: map[Date]bool{}, remove: map[Date]bool{}}
	for _, d := range add {
		c.add[d] = true
	}
	for _, d := range remove {
		c.remove[d] = true
	}
	return c
}

func (c Calendar) IsHoliday(d Date) bool {
	if c.remove[d] {
		return false
	}
	if c.add[d] {
		return true
	}
	for _, f := range c.fixed {
		if int(d.M) == f.Month && d.D == f.Day {
			return true
		}
	}
	return false
}

func (c Calendar) IsBusinessDay(d Date) bool {
	w := d.Weekday()
	return w != time.Saturday && w != time.Sunday && !c.IsHoliday(d)
}
