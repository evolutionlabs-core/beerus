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
	a := member("bank-a", "Member A", "999101", "901")
	for range 2 {
		if err := s.Register(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Institution(ctx, "999101")
	want := Institution{ID: "bank-a", Name: "Member A", NIPCode: "999101", NUBANCode: "901", Type: "commercial", Status: StatusActive}
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
	got, _ = s.Institution(ctx, "999101")
	if got.Name != "Member A Plc" || got.Type != "digital" || got.Status != StatusSuspended {
		t.Fatalf("rebrand: %+v", got)
	}
	dir, _ := s.Directory(ctx)
	if len(dir) != 5 {
		t.Fatalf("directory %d rows, want 5", len(dir))
	}
}

func TestRegisterRefusals(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	register(t, s, "bank-a", "Member A", "999101", "901")
	register(t, s, "bank-gone", "Gone Bank", "999109", "909")
	for _, st := range []string{StatusTerminating, StatusRemoved} {
		if err := s.SetStatus(ctx, "bank-gone", st); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		in   Institution
		want string
	}{
		{member("bank-a", "Member A", "999101", "911"), "bank bank-a has codes 999101/901 and they cannot change to 999101/911"},
		{member("bank-a", "Member A", "999111", "901"), "bank bank-a has codes 999101/901 and they cannot change to 999111/901"},
		{member("bank-x", "Member X", "999101", "931"), "bank bank-x: nip_code 999101 is held by bank-a: code or name already used"},
		{member("bank-x", "Member X", "999131", "901"), "bank bank-x: nuban_code 901 is held by bank-a: code or name already used"},
		{member("bank-x", "member a", "999131", "931"), `bank bank-x: name "member a" is held by bank-a: code or name already used`},
		{member("bank-x", "Member X", "999001", "931"), "bank bank-x: nip_code 999001 is held by outside bank Bank A: code or name already used"},
		{member("bank-x", "Bank C", "999131", "931"), `bank bank-x: name "Bank C" is held by outside bank Bank C: code or name already used`},
		{member("bank-x", "Member X", "999109", "931"), "bank bank-x: nip_code 999109 is held by removed bank bank-gone: code or name already used"},
		{member("bank-x", "Member X", "999131", "909"), "bank bank-x: nuban_code 909 is held by removed bank bank-gone: code or name already used"},
		{member("bank-x", "Gone Bank", "999131", "931"), `bank bank-x: name "Gone Bank" is held by removed bank bank-gone: code or name already used`},
		{member("bank-gone", "Gone Bank", "999109", "909"), "bank bank-gone was removed, and its id stays reserved: code or name already used"},
		{member("999003", "Member X", "999131", "931"), "999003 is the id of outside bank Bank C: code or name already used"},
		{Institution{ID: "bank-x", Name: "X", NIPCode: "999131", NUBANCode: "931", Type: "commercial", Outside: true}, "register adds member banks; bank-x is marked outside"},
		{member("bank-x", " ", "999131", "931"), "a member bank needs an id, a name and a type"},
		{member("bank-x", "X", "99913", "931"), `bank bank-x: nip_code "99913" must be 6 digits`},
		{member("bank-x", "X", "999131", "93"), `bank bank-x: nuban_code "93" must be 3 or 6 digits`},
	}
	for _, c := range cases {
		wantErr(t, s.Register(ctx, c.in), c.want)
	}
	suspended := member("bank-x", "X", "999131", "931")
	suspended.Status = StatusSuspended
	wantErr(t, s.Register(ctx, suspended), "bank bank-x registers as active, not suspended; use SetStatus to change it")
}
