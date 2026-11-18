package openbanking

import (
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

func lapsed(now, expires time.Time) bool {
	return now.After(expires)
}

func expiresAt(now time.Time, life time.Duration) time.Time {
	return now.Add(life)
}

func assertionWindow(exp, now time.Time, life time.Duration) bool {
	return !exp.Before(now) && !exp.After(expiresAt(now, life))
}

func addMonths(t time.Time, n int) time.Time {
	y, m, d := t.Date()
	first := time.Date(y, m+time.Month(n), 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
	if last := first.AddDate(0, 1, -1).Day(); d > last {
		d = last
	}
	return first.AddDate(0, 0, d-1)
}

func consentStatus(c *Consent, now time.Time, dormantDays int) Status {
	if c.Status != Authorised {
		return c.Status
	}
	if !now.Before(c.ExpiresAt) {
		return ReconsentRequired
	}
	lastUse := c.LastUsedAt
	if lastUse.Before(c.AuthorisedAt) {
		lastUse = c.AuthorisedAt
	}
	if !now.Before(lastUse.AddDate(0, 0, dormantDays)) {
		return Dormant
	}
	return Authorised
}

func asOfAllowed(d, today, floor clock.Date) bool {
	return !d.After(today.AddDays(-1)) && !d.Before(floor.AddDays(-1))
}
