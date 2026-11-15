package nibss

import (
	"context"
	"errors"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/nip"
)

func member(id, name, code, nuban string) Member {
	return Member{Institution: nip.Institution{ID: id, Name: name, NIPCode: code, NUBANCode: nuban, Type: "commercial"}}
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
	want := nip.Institution{ID: "bank-a", Name: "Member A", NIPCode: "999001", NUBANCode: "901", Type: "commercial", Status: nip.StatusActive}
	if err != nil || got != want {
		t.Fatalf("registered: %+v, %v", got, err)
	}
	if err := s.SetStatus(ctx, "bank-a", nip.StatusSuspended); err != nil {
		t.Fatal(err)
	}
	rebrand := a
	rebrand.Name, rebrand.Type = "Member A Plc", "digital"
	if err := s.Register(ctx, rebrand); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Institution(ctx, "999001")
	if got.Name != "Member A Plc" || got.Type != "digital" || got.Status != nip.StatusSuspended {
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
	for _, st := range []string{nip.StatusTerminating, nip.StatusRemoved} {
		if err := s.SetStatus(ctx, "bank-gone", st); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		in   Member
		want string
	}{
		{member("bank-a", "Member A", "999001", "911"), "bank bank-a is registered with nuban_code 901, and this request has nuban_code 911"},
		{member("bank-a", "Member A", "999011", "901"), "bank bank-a is registered with nip_code 999001, and this request has nip_code 999011"},
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
	suspended.Status = nip.StatusSuspended
	wantErr(t, s.Register(ctx, suspended), "bank bank-x registers as active, not suspended; use SetStatus to change it")
}

func TestRegisterContact(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	register(t, s, "bank-a", "Member A", "999001", "901")
	withContact := func(url, identity string) Member {
		m := member("bank-a", "Member A", "999001", "901")
		m.CreditURL, m.CertificateIdentity = url, identity
		return m
	}
	steps := []struct {
		name string
		in   Member
		want string
	}{
		{"fills in what was empty", withContact("https://a.localhost:1", "bank-a"), ""},
		{"repeat", withContact("https://a.localhost:1", "bank-a"), ""},
		{"other url", withContact("https://a.localhost:2", "bank-a"), "bank bank-a is registered with credit_url https://a.localhost:1, and this request has credit_url https://a.localhost:2"},
		{"other identity and url", withContact("https://a.localhost:2", "bank-z"), "bank bank-a is registered with credit_url https://a.localhost:1 and certificate_identity bank-a, and this request has credit_url https://a.localhost:2 and certificate_identity bank-z"},
	}
	for _, st := range steps {
		err := s.Register(ctx, st.in)
		if st.want == "" && err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		if st.want != "" {
			var conflict *RegistrationConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("%s: got %v, want a RegistrationConflictError", st.name, err)
			}
			wantErr(t, err, st.want)
		}
	}
	got, err := s.Member(ctx, "bank-a")
	if err != nil || got.CreditURL != "https://a.localhost:1" || got.CertificateIdentity != "bank-a" {
		t.Fatalf("member: %+v, %v", got, err)
	}
	for _, c := range []struct {
		identity string
		want     bool
	}{{"bank-a", true}, {"bank-z", false}} {
		if ok, err := s.Admits(ctx, c.identity); err != nil || ok != c.want {
			t.Fatalf("admits %s: %v, %v, want %v", c.identity, ok, err, c.want)
		}
	}
	for _, st := range []string{nip.StatusTerminating, nip.StatusRemoved} {
		if err := s.SetStatus(ctx, "bank-a", st); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := s.Admits(ctx, "bank-a"); err != nil || ok {
		t.Fatalf("a removed bank is admitted: %v, %v", ok, err)
	}
	_, err = s.Member(ctx, "bank-x")
	wantErr(t, err, "bank id bank-x is not in the NIBSS directory")
}
