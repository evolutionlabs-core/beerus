package connect

import (
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

func TestPlanSync(t *testing.T) {
	at := func(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, clock.Lagos) }
	const interval = time.Minute
	real := at(2026, time.November, 14, 10)
	rows := []struct {
		name    string
		conn    Connection
		now     time.Time
		due     bool
		expired bool
		at      time.Time
	}{
		{"synced a second ago", Connection{Status: "active", LastSyncedReal: real.Add(-time.Second)}, real, false, false, real},
		{"sync interval reached", Connection{Status: "active", LastSyncedReal: real.Add(-interval)}, real, true, false, real},
		{"never synced", Connection{Status: "active"}, real, true, false, real},
		{"syncing and due", Connection{Status: "syncing", SyncDueReal: real}, real, true, false, real},
		{"syncing and not yet due", Connection{Status: "syncing", SyncDueReal: real.Add(time.Second)}, real, false, false, time.Time{}},
		{"institution down retries", Connection{Status: "institution_down", LastSyncedReal: real}, real, true, false, real},
		{"no expiry", Connection{Status: "active", LastSyncedReal: real}, real, false, false, real},
		{"a millisecond before expiry", Connection{Status: "active", LastSyncedReal: real, ExpiresAt: real.Add(time.Millisecond)}, real, false, false, real},
		{"at expiry", Connection{Status: "active", LastSyncedReal: real, ExpiresAt: real}, real, true, true, real.Add(-time.Millisecond)},
		{"a day after expiry", Connection{Status: "active", LastSyncedReal: real, ExpiresAt: real.Add(-24 * time.Hour)}, real, true, true, real.Add(-24*time.Hour - time.Millisecond)},
		{"expires on a leap day", Connection{Status: "active", LastSyncedReal: at(2028, time.February, 29, 10), ExpiresAt: at(2028, time.February, 29, 10)}, at(2028, time.February, 29, 10), true, true, at(2028, time.February, 29, 10).Add(-time.Millisecond)},
		{"the day before a leap day expiry", Connection{Status: "active", LastSyncedReal: at(2028, time.February, 28, 10), ExpiresAt: at(2028, time.February, 29, 10)}, at(2028, time.February, 28, 10), false, false, at(2028, time.February, 28, 10)},
		{"expires on a month end", Connection{Status: "active", LastSyncedReal: at(2027, time.February, 28, 10), ExpiresAt: at(2027, time.February, 28, 10)}, at(2027, time.February, 28, 10), true, true, at(2027, time.February, 28, 10).Add(-time.Millisecond)},
		{"the day after a month end expiry", Connection{Status: "active", LastSyncedReal: at(2027, time.March, 1, 10), ExpiresAt: at(2027, time.February, 28, 10)}, at(2027, time.March, 1, 10), true, true, at(2027, time.February, 28, 10).Add(-time.Millisecond)},
		{"expires at year end", Connection{Status: "active", LastSyncedReal: at(2026, time.December, 31, 23), ExpiresAt: at(2027, time.January, 1, 0)}, at(2026, time.December, 31, 23), false, false, at(2026, time.December, 31, 23)},
		{"expiry far in the future", Connection{Status: "active", LastSyncedReal: real, ExpiresAt: at(2126, time.March, 1, 0)}, real, false, false, real},
	}
	for _, r := range rows {
		got := planSync(&r.conn, interval, r.now, real)
		if got.due != r.due || got.expired != r.expired || !got.at.Equal(r.at) {
			t.Errorf("%s: %+v, want due %v expired %v at %v", r.name, got, r.due, r.expired, r.at)
		}
	}
}
