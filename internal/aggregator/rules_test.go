package aggregator

import (
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
	"github.com/evolutionlabs-core/beerus/internal/config"
)

func day(y int, m time.Month, d int) clock.Date {
	return clock.Date{Y: y, M: m, D: d}
}

func TestHistoryStart(t *testing.T) {
	today := day(2026, time.November, 14)
	for _, r := range []struct {
		name   string
		start  clock.Date
		today  clock.Date
		days   int
		want   clock.Date
		capped bool
	}{
		{"on the floor", day(2026, time.August, 16), today, 90, day(2026, time.August, 16), false},
		{"a day before the floor", day(2026, time.August, 15), today, 90, day(2026, time.August, 16), true},
		{"a start in the future", day(2026, time.December, 1), today, 90, day(2026, time.December, 1), false},
		{"floor on a leap day", day(2028, time.February, 28), day(2028, time.March, 29), 29, day(2028, time.February, 29), true},
		{"floor across year end", day(2025, time.December, 1), day(2026, time.January, 5), 30, day(2025, time.December, 6), true},
		{"far-future today", day(2100, time.January, 1), day(2126, time.March, 1), 730, day(2124, time.March, 1), true},
		{"no history", day(2026, time.November, 13), today, 0, today, true},
	} {
		got, capped := historyStart(r.start, r.today, r.days)
		if got != r.want || capped != r.capped {
			t.Errorf("%s: %s capped %v, want %s capped %v", r.name, got, capped, r.want, r.capped)
		}
	}
}

func TestPlanSync(t *testing.T) {
	at := func(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, clock.Lagos) }
	const interval = time.Minute
	real := at(2026, time.November, 14, 10)
	leap := at(2028, time.February, 29, 10)
	for _, r := range []struct {
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
		{"a millisecond before expiry", Connection{Status: "active", LastSyncedReal: real, ExpiresAt: real.Add(time.Millisecond)}, real, false, false, real},
		{"at expiry", Connection{Status: "active", LastSyncedReal: real, ExpiresAt: real}, real, true, true, real.Add(-time.Millisecond)},
		{"expires on a leap day", Connection{Status: "active", LastSyncedReal: leap, ExpiresAt: leap}, leap, true, true, leap.Add(-time.Millisecond)},
		{"expiry far in the future", Connection{Status: "active", LastSyncedReal: real, ExpiresAt: at(2126, time.March, 1, 0)}, real, false, false, real},
	} {
		got := planSync(&r.conn, interval, r.now, real)
		if got.due != r.due || got.expired != r.expired || !got.at.Equal(r.at) {
			t.Errorf("%s: %+v, want due %v expired %v at %v", r.name, got, r.due, r.expired, r.at)
		}
	}
}

func TestEventIDsAreTrackedOnceAndBounded(t *testing.T) {
	var j jtiSet
	now := time.Now()
	if ok, err := j.first("bank-a|1", now.Add(time.Minute), now); !ok || err != nil {
		t.Fatalf("first sight: %v %v", ok, err)
	}
	if ok, _ := j.first("bank-a|1", now.Add(time.Minute), now); ok {
		t.Fatal("a repeated event id was accepted")
	}
	for i := 0; i < seenLimit; i++ {
		j.first("id-"+time.Duration(i).String(), now.Add(time.Hour), now)
	}
	if _, err := j.first("one-more", now.Add(time.Hour), now); err != errTooManyEvents {
		t.Fatalf("unbounded growth: %v", err)
	}
	later := now.Add(2 * time.Hour)
	if ok, err := j.first("after-expiry", later.Add(time.Minute), later); !ok || err != nil {
		t.Fatalf("expired ids were not dropped: %v %v", ok, err)
	}
	if floor := time.Duration(config.MinAccessSeconds) * time.Second; refreshAhead >= floor {
		t.Fatalf("refreshAhead %s must be shorter than the %s minimum access token life", refreshAhead, floor)
	}
}
