package world

import (
	"fmt"
	"testing"
	"time"
)

func TestGenerateRejectsHistoryOutsideTheBound(t *testing.T) {
	for _, days := range []int{-1, MaxHistoryDays + 1} {
		_, err := Generate(TemplateRetail, 1, nil, days, time.Time{}, nil)
		want := fmt.Sprintf("history is %d days; it must be 0 to 730", days)
		if err == nil || err.Error() != want {
			t.Fatalf("history of %d days: got %v, want %q", days, err, want)
		}
	}
}
