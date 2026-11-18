package connect

import "time"

type syncPlan struct {
	at      time.Time
	due     bool
	expired bool
}

func planSync(c *Connection, interval time.Duration, now, real time.Time) syncPlan {
	if c.Status == "syncing" && real.Before(c.SyncDueReal) {
		return syncPlan{}
	}
	p := syncPlan{at: now}
	p.expired = !c.ExpiresAt.IsZero() && !now.Before(c.ExpiresAt)
	if p.expired {
		p.at = c.ExpiresAt.Add(-time.Millisecond)
	}
	p.due = c.Status == "syncing" || c.Status == "institution_down" || p.expired ||
		real.Sub(c.LastSyncedReal) >= interval
	return p
}
