package clock

import (
	"testing"
	"time"
)

func TestBackoffDoublesToFiveMinutes(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	for i, w := range want {
		if got := Backoff(i + 1); got != w {
			t.Errorf("attempt %d: %v, want %v", i+1, got, w)
		}
	}
	if got := Backoff(40); got != 5*time.Minute {
		t.Fatalf("attempt 40: %v", got)
	}
}
