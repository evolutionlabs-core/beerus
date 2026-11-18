package openbanking

import (
	"testing"
	"time"
)

func TestEventBackoffGrowsAndStops(t *testing.T) {
	last := time.Duration(0)
	for n := 0; n < 40; n++ {
		d := Backoff(n)
		if d < last || d > eventBackoffAt {
			t.Fatalf("attempt %d waits %s after %s", n, d, last)
		}
		last = d
	}
	if Backoff(0) != eventBackoff || last != eventBackoffAt {
		t.Fatalf("backoff runs from %s to %s", Backoff(0), last)
	}
}
