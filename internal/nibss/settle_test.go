package nibss

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/nip"
)

func TestSettleGroupsByCutoffAndKeepsTheSchedule(t *testing.T) {
	ctx := t.Context()
	s := sessionStore(t)
	sched := Schedule{Per: 4}
	for i, id := range []string{"v1", "v2", "v3", "v4"} {
		if _, err := begin(ctx, s, transfer(id, "999002", t0)); err != nil {
			t.Fatal(err)
		}
		if err := s.Resolve(ctx, id, SessionOK, nip.OutcomeCredited, t0.Add(time.Duration(i*i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	banks := map[string]string{"999001": "bank-a", "999002": "bank-b"}
	type pass struct {
		name  string
		at    time.Duration
		adhoc bool
		keys  []string
		nets  []BankNet
	}
	passes := []pass{
		{"before the first cutoff", 2 * time.Hour, false, []string{}, nil},
		{"the cutoffs that have passed", 11 * time.Hour, false, []string{"2026-11-04T12:00", "2026-11-04T18:00"}, []BankNet{{"bank-a", -1000000}, {"bank-b", 1000000}}},
		{"the same pass again", 11 * time.Hour, false, []string{"2026-11-04T12:00", "2026-11-04T18:00"}, nil},
	}
	for _, p := range passes {
		got, err := s.Settle(ctx, sched, t0.Add(p.at), p.adhoc)
		keys := []string{}
		for _, x := range got {
			keys = append(keys, x.Key)
		}
		if err != nil || !reflect.DeepEqual(keys, p.keys) {
			t.Fatalf("%s: %v, %v, want %v", p.name, keys, err, p.keys)
		}
		if len(got) > 0 && p.nets != nil {
			nets, err := got[0].Nets(banks)
			if err != nil || !reflect.DeepEqual(nets, p.nets) || len(Moved(nets)) != 2 {
				t.Fatalf("%s: nets %+v, %v, want %+v", p.name, nets, err, p.nets)
			}
		}
	}
	if _, err := (Settlement{Key: "k", Sessions: []Session{transfer("x", "999002", t0)}}).Nets(map[string]string{"999001": "bank-a"}); err == nil || !strings.Contains(err.Error(), "session x is between 999001 and 999002, and one of them is not in the directory") {
		t.Fatalf("nets with a bank that is not in the directory: %v", err)
	}
	if unposted, err := s.SettledExcept(ctx, "999001", []string{}); err != nil || len(unposted) != 0 {
		t.Fatalf("settled before posting: %+v, %v", unposted, err)
	}
	if got := Moved([]BankNet{{"bank-a", -5}, {"bank-b", 0}, {"bank-c", 5}}); !reflect.DeepEqual(got, []BankNet{{"bank-a", -5}, {"bank-c", 5}}) {
		t.Fatalf("moved: %+v", got)
	}
	for _, key := range []string{"2026-11-04T12:00", "2026-11-04T18:00"} {
		if err := s.MarkPosted(ctx, key, t0); err != nil {
			t.Fatal(err)
		}
	}
	if posted, err := s.SettledExcept(ctx, "999001", []string{}); err != nil || len(posted) != 2 {
		t.Fatalf("settled after posting: %+v, %v", posted, err)
	}
	if n, err := s.PostedCount(ctx); err != nil || n != 2 {
		t.Fatalf("posted %d, %v", n, err)
	}
	got, err := s.Settle(ctx, sched, t0.Add(11*time.Hour), false)
	if err != nil || len(got) != 0 {
		t.Fatalf("after posting: %+v, %v", got, err)
	}
	if err := s.KeepSchedule(ctx, 4); err != nil {
		t.Fatal(err)
	}
	if err := s.KeepSchedule(ctx, 4); err != nil {
		t.Fatalf("keeping the same schedule twice: %v", err)
	}
	if err := s.KeepSchedule(ctx, 12); err == nil || err.Error() != "nip_sessions changed: NIBSS has settled with 4 sessions a day, so nip_sessions must stay 4" {
		t.Fatalf("a new schedule: %v", err)
	}
}
