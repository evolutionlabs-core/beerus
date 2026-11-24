package setup

import (
	"reflect"
	"testing"
)

func TestDefaultSpec(t *testing.T) {
	got := DefaultSpec("beerus")
	want := Spec{
		APIVersion: APIVersion,
		Kind:       KindSetup,
		Metadata:   Metadata{Name: "beerus"},
		Spec:       Body{NIP: NIP{Sessions: 4, TSQAfter: "2m"}},
	}
	if !reflect.DeepEqual(got, want) || len(got.pos) != 0 {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	fixture := clearPos(parseFixture(t, "default.yaml"))
	fixture.Spec.Connect = ConnectLegal{}
	fixture.Spec.Holidays = Holidays{}
	fixture.Spec.Banks = nil
	if !reflect.DeepEqual(got, fixture) {
		t.Fatalf("fixture differs:\n%+v\n%+v", got, fixture)
	}
	if errs := Validate(got); len(errs) > 0 {
		t.Fatal(errStrings(errs))
	}
	out, err := Export(DefaultSpec("lagos-dev"))
	if err != nil {
		t.Fatal(err)
	}
	back, errs := Parse("setup.yaml", out)
	if len(errs) > 0 || !reflect.DeepEqual(clearPos(back), DefaultSpec("lagos-dev")) {
		t.Fatalf("%v\n%s", errStrings(errs), out)
	}
}
