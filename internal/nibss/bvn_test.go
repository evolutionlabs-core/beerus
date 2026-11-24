package nibss

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

var adaezeDOB = clock.Date{Y: 1990, M: 4, D: 12}

func TestNormalise(t *testing.T) {
	for in, want := range map[string]string{
		"Adaeze Okafor": "ADAEZE OKAFOR", "  adaeze\tokafor  ": "ADAEZE OKAFOR", "Okafor Adaeze": "ADAEZE OKAFOR", "Adébáyọ̀ Ọlá": "ADEBAYO OLA",
		"O'Neil Chukwu": "CHUKWU ONEIL", "O’Neil Chukwu": "CHUKWU ONEIL", "Ade-Bayo Musa": "ADE BAYO MUSA", "Musa, Ade Bayo": "ADE BAYO MUSA", "Dr. Ngozi Eze": "DR EZE NGOZI",
	} {
		if got, err := normaliseName(in); err != nil || got != want {
			t.Errorf("normaliseName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "   ", "..'", "-"} {
		_, err := normaliseName(bad)
		wantBadDetails(t, err, `name "`+bad+`" has no letters`)
	}
	for _, in := range []string{"08031234567", "+2348031234567", "2348031234567", "+234 803 123 4567", "0803-123-4567", "(0803) 123 4567"} {
		if got, err := normalisePhone(in, "234", "0"); err != nil || got != "2348031234567" {
			t.Errorf("normalisePhone(%q) = %q, %v", in, got, err)
		}
	}
	digits, prefix := " may hold only digits, spaces, dashes, brackets and a leading +", " must start with +234 or 0"
	for in, want := range map[string]string{"0803x1234567": digits, "080+31234567": digits, "+448031234567": prefix, "8031234567": prefix, "0": prefix, "": prefix} {
		_, err := normalisePhone(in, "234", "0")
		wantBadDetails(t, err, `phone "`+in+`"`+want)
	}
	for serial, want := range map[int64]string{1: "22900000013", 2: "22900000021", 9999999: "22999999992"} {
		if got := makeBVN(serial); got != want {
			t.Errorf("makeBVN(%d) = %s, want %s", serial, got, want)
		}
	}
}

func TestEnrolBVN(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	count := func() int {
		var rows int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM bvns`).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	first, err := s.EnrolBVN(ctx, "Adaeze Okafor", "08031234567", adaezeDOB)
	if err != nil || first != makeBVN(1) {
		t.Fatalf("first BVN %s, %v, want %s", first, err, makeBVN(1))
	}
	for _, again := range []struct{ name, phone string }{{"ADAEZE OKAFOR", "+2348031234567"}, {"  okafor   adaeze ", "0803 123 4567"}} {
		if got, err := s.EnrolBVN(ctx, again.name, again.phone, adaezeDOB); err != nil || got != first {
			t.Errorf("EnrolBVN(%q, %q) = %s, %v; want %s", again.name, again.phone, got, err, first)
		}
	}
	if count() != 1 {
		t.Fatalf("%d bvn rows, want 1", count())
	}
	for i, o := range []struct {
		name, phone string
		dob         clock.Date
	}{
		{"Adaeze Okafor", "08031234568", adaezeDOB},
		{"Adaeze Okafor", "08031234567", clock.Date{Y: 1990, M: 4, D: 13}},
		{"Adaeze Okafor-Eze", "08031234567", adaezeDOB},
		{"Adaeze Okafor", "08031234567", clock.Date{Y: 2024, M: 2, D: 29}},
	} {
		if got, err := s.EnrolBVN(ctx, o.name, o.phone, o.dob); err != nil || got != makeBVN(int64(i+2)) {
			t.Errorf("EnrolBVN(%q, %q, %s) = %s, %v; want new BVN %s", o.name, o.phone, o.dob, got, err, makeBVN(int64(i+2)))
		}
	}
	for _, c := range []struct {
		name, phone string
		dob         clock.Date
		want        string
	}{
		{"Adaeze Okafor", "08031234567", clock.Date{Y: 2023, M: 2, D: 29}, "date of birth 2023-02-29 is not a real date"},
		{"Adaeze Okafor", "08031234567", clock.Date{}, "date of birth 0000-00-00 is not a real date"},
		{" ", "08031234567", adaezeDOB, `name " " has no letters`},
		{"Adaeze Okafor", "phone", adaezeDOB, `phone "phone" may hold only digits, spaces, dashes, brackets and a leading +`},
	} {
		_, err := s.EnrolBVN(ctx, c.name, c.phone, c.dob)
		wantBadDetails(t, err, c.want)
	}
	const n = 8
	got := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], errs[i] = s.EnrolBVN(ctx, "Chidi Obi", "08035550000", adaezeDOB)
		}()
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil || got[i] != got[0] {
			t.Fatalf("call %d: %s, %v; want %s", i, got[i], errs[i], got[0])
		}
	}
	if count() != 6 {
		t.Fatalf("%d bvn rows, want 6", count())
	}
	if _, err := s.pool.Exec(ctx, `SELECT setval('bvn_seq', 9999998)`); err != nil {
		t.Fatal(err)
	}
	last, err := s.EnrolBVN(ctx, "Tunde Bello", "08030000001", adaezeDOB)
	if err != nil || last != "22999999992" {
		t.Fatalf("last BVN %s, %v", last, err)
	}
	_, err = s.EnrolBVN(ctx, "Ngozi Eze", "08030000002", adaezeDOB)
	wantErr(t, err, "all 9999999 BVN serials are used")
	if got, err := s.EnrolBVN(ctx, "Tunde Bello", "08030000001", adaezeDOB); err != nil || got != last {
		t.Errorf("existing person after run out: %s, %v", got, err)
	}
}

func wantBadDetails(t *testing.T, err error, msg string) {
	t.Helper()
	if !errors.Is(err, ErrBadDetails) {
		t.Fatalf("got error %v, want it to wrap ErrBadDetails", err)
	}
	wantErr(t, err, ErrBadDetails.Error()+": "+msg)
}
