package clock

import (
	"errors"
	"sync"
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

type State struct {
	AnchorBusiness time.Time `json:"anchor_business"`
	AnchorReal     time.Time `json:"anchor_real"`
	Pace           float64   `json:"pace"`
}

type Clock struct {
	mu   sync.RWMutex
	st   State
	real func() time.Time
}

func New(st State, real func() time.Time) *Clock {
	if real == nil {
		real = time.Now
	}
	if st.Pace < 0 || (st.Pace == 0 && st.AnchorReal.IsZero()) {
		st.Pace = 1
	}
	if st.AnchorReal.IsZero() {
		now := real()
		st.AnchorReal = now
		if st.AnchorBusiness.IsZero() {
			st.AnchorBusiness = now
		}
	}
	return &Clock{st: st, real: real}
}

func (c *Clock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.at(c.real())
}

func (c *Clock) at(real time.Time) time.Time {
	elapsed := real.Sub(c.st.AnchorReal)
	if elapsed < 0 {
		elapsed = 0
	}
	return c.st.AnchorBusiness.Add(time.Duration(float64(elapsed) * c.st.Pace)).In(Lagos).Truncate(time.Millisecond)
}

func (c *Clock) Real() time.Time {
	return c.real()
}

func (c *Clock) Today() Date {
	return DateOf(c.Now())
}

func (c *Clock) rebase() {
	now := c.real()
	c.st.AnchorBusiness = c.at(now)
	c.st.AnchorReal = now
}

func (c *Clock) Advance(d time.Duration) (time.Time, error) {
	if d < 0 {
		return time.Time{}, errors.New("clock: cannot move backwards")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rebase()
	c.st.AnchorBusiness = c.st.AnchorBusiness.Add(d)
	return c.st.AnchorBusiness.In(Lagos), nil
}

func (c *Clock) AdvanceTo(t time.Time) (time.Time, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rebase()
	if t.Before(c.st.AnchorBusiness) {
		return time.Time{}, errors.New("clock: cannot move backwards")
	}
	c.st.AnchorBusiness = t
	return t.In(Lagos), nil
}

func (c *Clock) SetPace(p float64) error {
	if p < 0 || p > 100000 {
		return errors.New("clock: pace must be between 0 and 100000")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rebase()
	c.st.Pace = p
	return nil
}

func (c *Clock) Restore(st State) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.st = State{AnchorBusiness: st.AnchorBusiness, AnchorReal: c.real(), Pace: c.st.Pace}
}

func (c *Clock) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rebase()
	return c.st
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
