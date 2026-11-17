package nibss

import (
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

type Schedule struct {
	Per int
	Cal clock.Calendar
}

func (s Schedule) every() time.Duration {
	return 24 * time.Hour / time.Duration(s.Per)
}

func (s Schedule) Next(after time.Time) time.Time {
	d := clock.DateOf(after)
	k := int(after.Sub(d.Start())/s.every()) + 1
	for {
		if s.Cal.IsBusinessDay(d) && k <= s.Per {
			return d.Start().Add(time.Duration(k) * s.every())
		}
		d, k = d.AddDays(1), 1
	}
}

func (s Schedule) Prev(at time.Time) time.Time {
	d := clock.DateOf(at)
	k := int(at.Sub(d.Start()) / s.every())
	for {
		if s.Cal.IsBusinessDay(d) && k >= 1 {
			return d.Start().Add(time.Duration(k) * s.every())
		}
		d, k = d.AddDays(-1), s.Per
	}
}

func (s Schedule) Key(cutoff time.Time) string {
	return cutoff.In(clock.Lagos).Format("2006-01-02T15:04")
}
