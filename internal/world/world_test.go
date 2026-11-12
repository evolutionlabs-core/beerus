package world

import "testing"

func TestNUBANValid(t *testing.T) {
	for _, serial := range []uint64{0, 1, 42, 999999999} {
		n := NUBAN("901", serial)
		if !ValidNUBAN("901", n) {
			t.Fatalf("NUBAN %s from serial %d is not valid", n, serial)
		}
		bad := n[:9] + string('0'+(n[9]-'0'+1)%10)
		if ValidNUBAN("901", bad) {
			t.Fatalf("NUBAN %s with a wrong check digit was accepted", bad)
		}
	}
	if got := NUBAN("901", 1); got != "0000000017" {
		t.Fatalf("NUBAN(901, 1) = %s, want 0000000017", got)
	}
}

func TestID(t *testing.T) {
	a, b := ID("cus", "bank-a", "k1"), ID("cus", "bank-a", "k1")
	if a != b || len(a) != len("cus_")+20 || a[:4] != "cus_" {
		t.Fatalf("ID gave %q and %q", a, b)
	}
	if ID("cus", "bank-a", "k1") == ID("cus", "bank-a", "k2") || ID("cus", "bank-ak", "1") == ID("cus", "bank-a", "k1") {
		t.Fatal("different parts gave the same ID")
	}
}

func TestNUBANSixDigitSeed(t *testing.T) {
	for _, serial := range []uint64{0, 1, 123456789, 987654321} {
		if NUBAN("901", serial) != NUBAN("000901", serial) {
			t.Fatalf("3-digit code %d differs from its 6-digit seed", serial)
		}
		if NUBAN("50211", serial) != NUBAN("950211", serial) {
			t.Fatalf("5-digit code %d differs from its 6-digit seed", serial)
		}
		if n := NUBAN("950211", serial); !ValidNUBAN("950211", n) || len(n) != 10 {
			t.Fatalf("6-digit code gave %s", n)
		}
	}
}

func TestNormaliseBankCode(t *testing.T) {
	for code, want := range map[string]string{"901": "000901", "000901": "000901", "50211": "950211", "950211": "950211"} {
		if got, err := NormaliseBankCode(code); err != nil || got != want {
			t.Errorf("%s: %q %v", code, got, err)
		}
	}
	for _, code := range []string{"", "12", "1234", "1234567", "90a", "9 1"} {
		if _, err := NormaliseBankCode(code); err == nil {
			t.Errorf("%q was accepted", code)
		}
		if ValidNUBAN(code, "0123456789") {
			t.Errorf("%q validated an account number", code)
		}
	}
	if ValidNUBAN("901", "01234567x9") || ValidNUBAN("901", "012345678") {
		t.Fatal("a malformed account number was accepted")
	}
}
