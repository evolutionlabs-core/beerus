package nibss

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/nip"
)

func TestSettlementProblems(t *testing.T) {
	ctx := t.Context()
	sched := Schedule{Per: 4}
	noon := time.Date(2026, 11, 4, 12, 0, 0, 0, t0.Location())
	rows := []struct {
		name       string
		resolvedAt []time.Duration
		settled    bool
		checkedAt  time.Time
		want       []string
	}{
		{"nothing yet", nil, false, noon.Add(time.Hour), []string{}},
		{"inside the grace", []time.Duration{0, time.Hour}, false, noon.Add(30 * time.Second), []string{}},
		{"two transfers past the grace", []time.Duration{0, time.Hour}, false, noon.Add(61 * time.Second), []string{"2 transfers resolved before the 2026-11-04 12:00 cutoff are not settled yet"}},
		{"one transfer past the grace", []time.Duration{0, 4 * time.Hour}, false, noon.Add(61 * time.Second), []string{"1 transfer resolved before the 2026-11-04 12:00 cutoff is not settled yet"}},
		{"settled but not posted", []time.Duration{0}, true, noon.Add(time.Hour), []string{"settlement session 2026-11-04T12:00 was cut off at 2026-11-04T12:00:00+01:00 and is not posted to the CBN yet"}},
		{"settled inside the grace", []time.Duration{0}, true, noon.Add(30 * time.Second), []string{}},
	}
	for _, r := range rows {
		s := sessionStore(t)
		for i, d := range r.resolvedAt {
			id := fmt.Sprintf("transfer-%d", i+1)
			if _, err := begin(ctx, s, transfer(id, "999002", t0)); err != nil {
				t.Fatal(err)
			}
			if err := s.Resolve(ctx, id, SessionOK, nip.OutcomeCredited, t0.Add(d)); err != nil {
				t.Fatal(err)
			}
		}
		if r.settled {
			if _, err := s.Settle(ctx, sched, noon.Add(time.Minute), false); err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.SettlementProblems(ctx, sched, r.checkedAt)
		if err != nil || !reflect.DeepEqual(got, r.want) {
			t.Errorf("%s: %q, %v, want %q", r.name, got, err, r.want)
		}
	}
}
