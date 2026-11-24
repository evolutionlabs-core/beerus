package control

import (
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

func TestFaultLiveUntilItsEndTime(t *testing.T) {
	start := time.Date(2026, time.November, 14, 10, 0, 0, 0, clock.Lagos)
	req := FaultRequest{Kind: FaultDown, For: "1h"}
	f, err := NewFault(req, start)
	if err != nil || f.Until == nil || !f.Until.Equal(start.Add(time.Hour)) {
		t.Fatalf("fault %+v, %v, want it to end at %v", f, err, start.Add(time.Hour))
	}
	forever, err := NewFault(FaultRequest{Kind: FaultDown}, start)
	if err != nil || forever.Until != nil {
		t.Fatalf("a down fault without for: %+v, %v", forever, err)
	}
	count := 2
	timeout, err := NewFault(FaultRequest{Kind: FaultTimeout, Outcome: "credited", Count: &count}, start)
	if err != nil {
		t.Fatal(err)
	}
	spent := timeout
	spent.Left = 0
	for _, c := range []struct {
		name  string
		fault Fault
		now   time.Time
		want  bool
	}{
		{"a nanosecond before the end", f, start.Add(time.Hour - time.Nanosecond), true},
		{"at the end", f, start.Add(time.Hour), false},
		{"without an end, a century on", forever, start.AddDate(100, 0, 0), true},
		{"a timeout with no uses left", spent, start, false},
	} {
		if got := c.fault.Live(c.now); got != c.want {
			t.Errorf("%s: live %v, want %v", c.name, got, c.want)
		}
	}
	var faults Faults
	faults.Add(f, start)
	if !faults.Down(start.Add(time.Hour-time.Second)) || faults.Down(start.Add(time.Hour)) {
		t.Errorf("down just before and at the end")
	}
	if len(faults.Live(start.Add(time.Hour))) != 0 {
		t.Error("a live fault at the end")
	}
	faults.Prune(start.Add(time.Hour))
	if got := faults.Clear(start); got != 0 {
		t.Errorf("cleared %d faults after pruning the ended one", got)
	}
}
