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
	if err != nil {
		t.Fatal(err)
	}
	if f.Until == nil || !f.Until.Equal(start.Add(time.Hour)) || !f.At.Equal(start) {
		t.Fatalf("fault %+v, want it to end at %v", f, start.Add(time.Hour))
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
		{"just created", f, start, true},
		{"a second before the end", f, start.Add(time.Hour - time.Second), true},
		{"a nanosecond before the end", f, start.Add(time.Hour - time.Nanosecond), true},
		{"at the end", f, start.Add(time.Hour), false},
		{"a second after the end", f, start.Add(time.Hour + time.Second), false},
		{"a day after the end", f, start.Add(25 * time.Hour), false},
		{"without an end, a century on", forever, start.AddDate(100, 0, 0), true},
		{"a timeout with uses left, a week on", timeout, start.AddDate(0, 0, 7), true},
		{"a timeout with no uses left", spent, start, false},
		{"an unknown kind", Fault{Kind: "other"}, start, false},
	} {
		if got := c.fault.Live(c.now); got != c.want {
			t.Errorf("%s: live %v, want %v", c.name, got, c.want)
		}
	}
	var faults Faults
	down, _ := NewFault(req, start)
	faults.Add(down, start)
	if !faults.Down(start.Add(time.Hour-time.Second)) || faults.Down(start.Add(time.Hour)) {
		t.Errorf("down just before and at the end: %v and %v", faults.Down(start.Add(time.Hour-time.Second)), faults.Down(start.Add(time.Hour)))
	}
	if got := len(faults.Live(start.Add(time.Hour))); got != 0 {
		t.Errorf("%d live faults at the end", got)
	}
	faults.Prune(start.Add(time.Hour))
	if got := faults.Clear(start); got != 0 {
		t.Errorf("cleared %d faults after pruning the ended one", got)
	}
}
