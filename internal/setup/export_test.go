package setup

import (
	"reflect"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

func TestDefaultSpecLeavesCodesAndLegalFieldsOut(t *testing.T) {
	got := DefaultSpec("beerus", clock.Date{Y: 2026, M: time.November, D: 4})
	want := Spec{
		APIVersion: APIVersion,
		Kind:       KindSetup,
		Metadata:   Metadata{Name: "beerus"},
		Spec: Body{
			Start: "2026-11-04T00:00:00+01:00",
			NIP:   NIP{Sessions: 4, TSQAfter: "2m", TSQAttempts: 3},
			Banks: []Bank{
				{ID: "bank-a", Name: "Bank A", Type: "commercial"},
				{ID: "bank-b", Name: "Bank B", Type: "digital"},
			},
		},
	}
	if !reflect.DeepEqual(clearPos(got), want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	if len(got.pos) != 0 {
		t.Fatalf("default spec carries positions from the embedded file: %v", got.pos)
	}
}

func TestDefaultSpecReturnsAFreshCopy(t *testing.T) {
	today := clock.Date{Y: 2026, M: time.November, D: 4}
	a := DefaultSpec("beerus", today)
	a.Spec.Banks[0].Name = "Changed"
	if b := DefaultSpec("beerus", today); b.Spec.Banks[0].Name != "Bank A" {
		t.Fatalf("second call saw the first call's change: %s", b.Spec.Banks[0].Name)
	}
}

func TestDefaultSpecMatchesTheFixtureApartFromPinnedFields(t *testing.T) {
	got := DefaultSpec("beerus", clock.Date{Y: 2026, M: time.November, D: 4})
	fixture := clearPos(parseFixture(t, "default.yaml"))
	fixture.Spec.Start = got.Spec.Start
	fixture.Spec.Connect = ConnectLegal{}
	fixture.Spec.Holidays = Holidays{}
	for i := range fixture.Spec.Banks {
		fixture.Spec.Banks[i].NIPCode, fixture.Spec.Banks[i].NUBANCode = "", ""
	}
	if !reflect.DeepEqual(got, fixture) {
		t.Fatalf("got  %+v\nwant %+v", got, fixture)
	}
}

func TestDefaultSpecStartsAtMidnightInLagos(t *testing.T) {
	cases := map[clock.Date]string{
		{Y: 2028, M: time.February, D: 29}: "2028-02-29T00:00:00+01:00",
		{Y: 2026, M: time.December, D: 31}: "2026-12-31T00:00:00+01:00",
		{Y: 2399, M: time.January, D: 1}:   "2399-01-01T00:00:00+01:00",
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
