package setup

import (
	"reflect"
	"testing"
)

func TestDefaultSpecLeavesCodesAndLegalFieldsOut(t *testing.T) {
	got := DefaultSpec("beerus")
	want := Spec{
		APIVersion: APIVersion,
		Kind:       KindSetup,
		Metadata:   Metadata{Name: "beerus"},
		Spec: Body{
			NIP: NIP{Sessions: 4, TSQAfter: "2m", TSQAttempts: 3},
		},
	}
	if !reflect.DeepEqual(clearPos(got), want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	if len(got.pos) != 0 {
		t.Fatalf("default spec carries positions from the embedded file: %v", got.pos)
	}
}

func TestDefaultSpecMatchesTheFixtureApartFromPinnedFields(t *testing.T) {
	got := DefaultSpec("beerus")
	fixture := clearPos(parseFixture(t, "default.yaml"))
	fixture.Spec.Connect = ConnectLegal{}
	fixture.Spec.Holidays = Holidays{}
	fixture.Spec.Banks = nil
	if !reflect.DeepEqual(got, fixture) {
		t.Fatalf("got  %+v\nwant %+v", got, fixture)
	}
}

func TestDefaultSpecExportsAndParsesClean(t *testing.T) {
	s := DefaultSpec("lagos-dev")
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
