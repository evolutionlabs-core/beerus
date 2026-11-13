package nibss

import (
	"context"
	"testing"
)

func member(id, name, code, nuban string) Institution {
	return Institution{ID: id, Name: name, NIPCode: code, NUBANCode: nuban, Type: "commercial"}
}

func TestRegister(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	a := member("bank-a", "Member A", "999001", "901")
	for range 2 {
		if err := s.Register(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Institution(ctx, "999001")
	want := Institution{ID: "bank-a", Name: "Member A", NIPCode: "999001", NUBANCode: "901", Type: "commercial", Status: StatusActive}
	if err != nil || got != want {
		t.Fatalf("registered: %+v, %v", got, err)
	}
	if err := s.SetStatus(ctx, "bank-a", StatusSuspended); err != nil {
		t.Fatal(err)
	}
	rebrand := a
	rebrand.Name, rebrand.Type = "Member A Plc", "digital"
	if err := s.Register(ctx, rebrand); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Institution(ctx, "999001")
	if got.Name != "Member A Plc" || got.Type != "digital" || got.Status != StatusSuspended {
		t.Fatalf("rebrand: %+v", got)
	}
	dir, _ := s.Directory(ctx)
	if len(dir) != 1 {
		t.Fatalf("directory %d rows, want 1", len(dir))
	}
}

func TestRegisterRefusals(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	register(t, s, "bank-a", "Member A", "999001", "901")
	register(t, s, "bank-gone", "Gone Bank", "999009", "909")
	for _, st := range []string{StatusTerminating, StatusRemoved} {
		if err := s.SetStatus(ctx, "bank-gone", st); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		in   Institution
		want string
	}{
		{member("bank-a", "Member A", "999001", "911"), "bank bank-a has codes 999001/901 and they cannot change to 999001/911"},
		{member("bank-a", "Member A", "999011", "901"), "bank bank-a has codes 999001/901 and they cannot change to 999011/901"},
		{member("bank-x", "Member X", "999001", "931"), "bank bank-x: nip_code 999001 is held by bank-a: code or name already used"},
		{member("bank-x", "Member X", "999031", "901"), "bank bank-x: nuban_code 901 is held by bank-a: code or name already used"},
		{member("bank-x", "member a", "999031", "931"), `bank bank-x: name "member a" is held by bank-a: code or name already used`},
		{member("bank-x", "Member X", "999009", "931"), "bank bank-x: nip_code 999009 is held by removed bank bank-gone: code or name already used"},
		{member("bank-x", "Member X", "999031", "909"), "bank bank-x: nuban_code 909 is held by removed bank bank-gone: code or name already used"},
		{member("bank-gone", "Gone Bank", "999009", "909"), "bank bank-gone was removed, and its id stays reserved: code or name already used"},
		{member("bank-x", " ", "999031", "931"), "a bank needs an id, a name and a type"},
		{member("bank-x", "X", "99913", "931"), `bank bank-x: nip_code "99913" must be 6 digits`},
		{member("bank-x", "X", "999031", "93"), `bank bank-x: nuban_code "93" must be 3, 5 or 6 digits`},
	}
	for _, c := range cases {
		wantErr(t, s.Register(ctx, c.in), c.want)
	}
	if err := s.Register(ctx, member("bank-y", "Gone Bank", "999032", "932")); err != nil {
		t.Fatalf("a removed bank's name is free again: %v", err)
	}
	suspended := member("bank-x", "X", "999031", "931")
	suspended.Status = StatusSuspended
	wantErr(t, s.Register(ctx, suspended), "bank bank-x registers as active, not suspended; use SetStatus to change it")
}
