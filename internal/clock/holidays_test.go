package clock

import "testing"

func mustDate(t *testing.T, s string) Date {
	t.Helper()
	d, err := ParseDate(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestHolidays(t *testing.T) {
	cases := map[string]bool{
		"2023-01-01": false,
		"2023-01-02": true,
		"2023-04-07": true,
		"2023-04-10": true,
		"2023-10-02": true,
		"2024-03-29": true,
		"2024-04-01": true,
		"2025-04-18": true,
		"2025-06-06": true,
		"2026-10-01": true,
		"2026-12-25": true,
		"2026-12-26": false,
		"2026-12-28": true,
		"2027-03-26": true,
		"2027-12-27": true,
		"2027-12-28": true,
		"2028-01-03": true,
		"2028-04-14": true,
		"2028-10-02": true,
		"2026-10-02": false,
		"2026-11-03": false,
	}
	for s, want := range cases {
		if got := IsHoliday(mustDate(t, s)); got != want {
			t.Errorf("%s: holiday %v, want %v", s, got, want)
		}
	}
}

func TestEaster(t *testing.T) {
	for y, want := range map[int]string{2023: "2023-04-09", 2024: "2024-03-31", 2025: "2025-04-20", 2026: "2026-04-05", 2027: "2027-03-28", 2028: "2028-04-16"} {
		if got := easter(y).String(); got != want {
			t.Errorf("%d: %s, want %s", y, got, want)
		}
	}
}

func TestIsBusinessDay(t *testing.T) {
	cases := map[string]bool{
		"2026-10-01": false,
		"2026-10-02": true,
		"2026-10-03": false,
		"2026-10-04": false,
		"2026-10-05": true,
		"2025-12-25": false,
	}
	for s, want := range cases {
		if got := IsBusinessDay(mustDate(t, s)); got != want {
			t.Errorf("%s: business day %v, want %v", s, got, want)
		}
	}
}
