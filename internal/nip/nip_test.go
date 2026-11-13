package nip

import (
	"strings"
	"testing"
	"time"
)

func TestDirectoryPrefersMembers(t *testing.T) {
	d := NewDirectory([]Participant{{Code: "999001", Name: "Bank A", BankID: "bank-a"}}, []Participant{{Code: "999001", Name: "Other"}, {Code: "999003", Name: "Bank C"}})
	if p, ok := d.Lookup("999001"); !ok || p.Outside || p.BankID != "bank-a" {
		t.Fatalf("member: %+v", p)
	}
	if p, ok := d.Lookup("999003"); !ok || !p.Outside {
		t.Fatalf("outside: %+v", p)
	}
	if _, ok := d.Lookup("000000"); ok || len(d.List()) != 2 {
		t.Fatal("unknown code")
	}
}

func TestBVNAndSessionID(t *testing.T) {
	a, b := BVN(1, "adaeze"), BVN(1, "ngozi")
	if len(a) != 11 || !strings.HasPrefix(a, BVNPrefix) || a == b || a != BVN(1, "adaeze") || a == BVN(2, "adaeze") {
		t.Fatalf("bvn %s %s", a, b)
	}
	at := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	s := SessionID(1, "999001", at, 0)
	if !ValidSessionID(s) || !strings.HasPrefix(s, "999001261103110000") || s == SessionID(1, "999001", at, 1) || s != SessionID(1, "999001", at, 0) {
		t.Fatalf("session id %s", s)
	}
}

func TestDefaultCap(t *testing.T) {
	if DefaultCap("microfinance") >= DefaultCap("commercial") || DefaultCap("digital") != DefaultCap("commercial") {
		t.Fatal("a microfinance bank should have a lower default cap than a commercial or digital bank")
	}
}
