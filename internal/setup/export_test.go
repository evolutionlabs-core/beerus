package setup

import (
	"reflect"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

func TestDefaultSpecMatchesTheDefaultFixture(t *testing.T) {
	got := DefaultSpec("beerus", clock.Date{Y: 2026, M: time.November, D: 4})
	want := clearPos(parseFixture(t, "default.yaml"))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func TestDefaultSpecStartsAtEightInLagos(t *testing.T) {
	cases := map[clock.Date]string{
		{Y: 2028, M: time.February, D: 29}: "2028-02-29T08:00:00+01:00",
		{Y: 2026, M: time.December, D: 31}: "2026-12-31T08:00:00+01:00",
		{Y: 2399, M: time.January, D: 1}:   "2399-01-01T08:00:00+01:00",
	}
	for today, want := range cases {
		if got := DefaultSpec("lagos-dev", today).Spec.Start; got != want {
			t.Errorf("DefaultSpec(%s).Start = %s, want %s", today, got, want)
		}
	}
}

func TestDefaultSpecExportsAndParsesClean(t *testing.T) {
	s := DefaultSpec("lagos-dev", clock.Date{Y: 2026, M: time.November, D: 7})
	out, err := Export(s)
	if err != nil {
		t.Fatal(err)
	}
	back, errs := Parse("setup.yaml", out)
	if len(errs) > 0 {
		t.Fatalf("%v\n%s", errStrings(errs), out)
	}
	if !reflect.DeepEqual(clearPos(back), s) {
		t.Fatalf("got %+v", back)
	}
}
