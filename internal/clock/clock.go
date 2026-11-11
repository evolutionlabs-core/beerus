package clock

import (
	"errors"
	"time"
)

var Lagos = mustLagos()

func mustLagos() *time.Location {
	loc, err := time.LoadLocation("Africa/Lagos")
	if err != nil {
		return time.FixedZone("WAT", 3600)
	}
	return loc
}

type Clock struct {
	real func() time.Time
}

func New(real func() time.Time) *Clock {
	if real == nil {
		real = time.Now
	}
	return &Clock{real: real}
}

func (c *Clock) Now() time.Time {
	return c.real().In(Lagos).Truncate(time.Millisecond)
}

func (c *Clock) Real() time.Time {
	return c.real()
}

func (c *Clock) Today() Date {
	return DateOf(c.Now())
}

type Date struct {
	Y int
	M time.Month
	D int
}

func DateOf(t time.Time) Date {
	y, m, d := t.In(Lagos).Date()
	return Date{y, m, d}
}

func ParseDate(s string) (Date, error) {
	t, err := time.ParseInLocation("2006-01-02", s, Lagos)
	if err != nil {
		return Date{}, err
	}
	if t.Format("2006-01-02") != s {
		return Date{}, errors.New("clock: invalid date")
	}
	return DateOf(t), nil
}

func (d Date) Start() time.Time {
	return time.Date(d.Y, d.M, d.D, 0, 0, 0, 0, Lagos)
}

func (d Date) End() time.Time {
	return d.AddDays(1).Start()
}

func (d Date) AddDays(n int) Date {
	return DateOf(time.Date(d.Y, d.M, d.D+n, 12, 0, 0, 0, Lagos))
}

func (d Date) String() string {
	return d.Start().Format("2006-01-02")
}

func (d Date) Before(o Date) bool {
	return d.Start().Before(o.Start())
}

func (d Date) After(o Date) bool {
	return o.Before(d)
}

func (d Date) Weekday() time.Weekday {
	return d.Start().Weekday()
}

func (d Date) IsZero() bool {
	return d.Y == 0
}

func (d Date) MarshalText() ([]byte, error) {
	if d.IsZero() {
		return []byte(""), nil
	}
	return []byte(d.String()), nil
}

func (d *Date) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		*d = Date{}
		return nil
	}
	p, err := ParseDate(string(b))
	if err != nil {
		return err
	}
	*d = p
	return nil
}

func DaysBetween(a, b Date) int {
	return int(b.Start().Sub(a.Start()).Hours()/24 + 0.5)
}
