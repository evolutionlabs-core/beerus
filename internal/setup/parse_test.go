package setup

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func errStrings(errs []error) []string {
	out := make([]string, len(errs))
	for i, e := range errs {
		out[i] = e.Error()
	}
	return out
}

func parseFixture(t *testing.T, name string) Spec {
	t.Helper()
	s, errs := Parse("setup.yaml", readFixture(t, name))
	if len(errs) > 0 {
		t.Fatalf("%s: %v", name, errStrings(errs))
	}
	return s
}

func TestParseGoodFixtures(t *testing.T) {
	d := parseFixture(t, "default.yaml")
	if d.Metadata.Name != "beerus" || len(d.Spec.Banks) != 2 || d.Spec.NIP.TSQAfter != "2m" || d.Spec.Connect.RegistryID != "OB-0001" {
		t.Fatalf("default: %+v", d)
	}
	three := parseFixture(t, "three.yaml")
	b := three.Spec.Banks[1]
	if b.NetDebitCap != 50000000000 || b.OnRemove == nil || b.OnRemove.Money != MoneyTransfer || b.OnRemove.To != "bank-c" {
		t.Fatalf("bank-b: %+v", b)
	}
	if three.Spec.Banks[2].NIPCode != "" {
		t.Fatalf("three: %+v", three.Spec)
	}
	if !slices.Equal(three.Spec.Holidays.Add, []string{"2026-12-28"}) {
		t.Fatalf("holidays: %v", three.Spec.Holidays.Add)
	}
	if p := three.Spec.Partners[0]; p.ID != "balance" || p.WebhookURL != "https://localhost:3000/webhooks" || len(p.RedirectURIs) != 1 {
		t.Fatalf("partner: %+v", p)
	}
}

func TestParseBadFixtures(t *testing.T) {
	want := map[string][]string{
		"bad-name.yaml": {
			`setup.yaml:4:3: metadata.name "Beerus_Setup" is not valid. Use 2 to 31 lowercase letters, digits or hyphens, starting with a letter.`,
		},
		"closed-without-on-remove.yaml": {
			`setup.yaml:29:7: banks[bank-b].state closed needs on_remove. Add on_remove with money: transfer, payout or leave.`,
		},
		"duplicate-bank.yaml": {
			`setup.yaml:24:7: banks[1].id bank-a is already used by another bank. Give each bank its own id.`,
		},
		"float-amount.yaml": {
			`setup.yaml:24:28: banks[bank-a].net_debit_cap_minor 1.5 is not a whole number. Give the amount in kobo, such as 150 for 1.50 naira.`,
		},
		"remove-to-unknown.yaml": {
			`setup.yaml:29:36: banks[bank-b].on_remove.to "bank-z" is not a bank in this file. Name a bank listed under banks.`,
		},
		"two-errors.yaml": {
			`setup.yaml:8:5: nip.tsq_after "soon" is not a duration. Use a duration such as 2m.`,
			`setup.yaml:26:7: banks[bank-b].type "online" is not a bank type. Use commercial, digital or microfinance.`,
		},
		"unknown-field.yaml": {
			`setup.yaml:7:5: unknown field "sesions" (did you mean "sessions"?)`,
		},
		"wrong-version.yaml": {
			`setup.yaml:1:1: apiVersion "beerus.dev/v2" is not supported. Use beerus.dev/v1.`,
		},
	}
	files, err := filepath.Glob(filepath.Join("testdata", "bad", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(want) {
		t.Fatalf("testdata/bad has %d files, the table has %d", len(files), len(want))
	}
	for _, f := range files {
		name := filepath.Base(f)
		t.Run(name, func(t *testing.T) {
			exp, ok := want[name]
			if !ok {
				t.Fatalf("no expected message for %s", name)
			}
			_, errs := Parse("setup.yaml", readFixture(t, filepath.Join("bad", name)))
			if got := errStrings(errs); !slices.Equal(got, exp) {
				t.Fatalf("got  %q\nwant %q", got, exp)
			}
		})
	}
}

func TestTypoGivesLineColumnAndSuggestion(t *testing.T) {
	_, errs := Parse("setup.yaml", readFixture(t, "bad/unknown-field.yaml"))
	got := errStrings(errs)
	want := []string{`setup.yaml:7:5: unknown field "sesions" (did you mean "sessions"?)`}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
}

func TestParseStoredIgnoresStartFromOlderBeerus(t *testing.T) {
	old := defaultWith(t, "  nip:\n", "  start: 2026-11-04T08:00:00+01:00\n  nip:\n")
	got, errs := ParseStored("setup.yaml", old)
	if len(errs) > 0 {
		t.Fatal(errStrings(errs))
	}
	want, errs := Parse("setup.yaml", readFixture(t, "default.yaml"))
	if len(errs) > 0 {
		t.Fatal(errStrings(errs))
	}
	if !reflect.DeepEqual(clearPos(got), clearPos(want)) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestTwoErrorsReportedInOneRun(t *testing.T) {
	_, errs := Parse("setup.yaml", readFixture(t, "bad/two-errors.yaml"))
	if len(errs) != 2 {
		t.Fatalf("got %d errors: %q", len(errs), errStrings(errs))
	}
}

func TestParseKeepsPositions(t *testing.T) {
	s := parseFixture(t, "three.yaml")
	cases := map[string]Pos{
		"":                              {"setup.yaml", 1, 1},
		"spec":                          {"setup.yaml", 5, 1},
		"nip.sessions":                  {"setup.yaml", 7, 5},
		"holidays.add[2026-12-28]":      {"setup.yaml", 9, 11},
		"banks[bank-b]":                 {"setup.yaml", 16, 7},
		"banks[bank-b].nip_code":        {"setup.yaml", 19, 7},
		"banks[bank-b].on_remove.to":    {"setup.yaml", 22, 36},
		"partners[balance].webhook_url": {"setup.yaml", 32, 7},
	}
	for path, want := range cases {
		got, ok := s.Pos(path)
		if !ok || got != want {
			t.Errorf("Pos(%q) = %v, %v; want %v", path, got, ok, want)
		}
	}
	if _, ok := s.Pos("banks[bank-c].nip_code"); ok {
		t.Error("a missing field has a position")
	}
}

func defaultWith(t *testing.T, old, new string) []byte {
	t.Helper()
	raw := string(readFixture(t, "default.yaml"))
	if !strings.Contains(raw, old) {
		t.Fatalf("default.yaml has no %q", old)
	}
	return []byte(strings.Replace(raw, old, new, 1))
}

func TestParseRejects(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
		want []string
	}{
		{"empty", []byte(""), []string{`setup.yaml: the file is empty. Write the settings in it.`}},
		{"two documents", append(readFixture(t, "default.yaml"), []byte("---\nkind: Setup\n")...), []string{`setup.yaml:30:1: the file holds more than one document. Keep one setup per file.`}},
		{"scalar root", []byte("hello\n"), []string{`setup.yaml:1:1: must be a mapping of fields. Write it as key: value lines.`}},
		{"repeated key", defaultWith(t, "  name: beerus\n", "  name: beerus\n  name: other\n"), []string{`setup.yaml:5:3: metadata.name appears twice. Keep one.`}},
		{"zero sessions", defaultWith(t, "sessions: 4", "sessions: 0"), []string{`setup.yaml:7:5: nip.sessions 0 does not split the day into whole minutes. Use a number such as 1, 2, 4, 6, 12 or 24.`}},
		{"uneven sessions", defaultWith(t, "sessions: 4", "sessions: 7"), []string{`setup.yaml:7:5: nip.sessions 7 does not split the day into whole minutes. Use a number such as 1, 2, 4, 6, 12 or 24.`}},
		{"retired start", defaultWith(t, "  nip:\n", "  start: 2026-11-04T08:00:00+01:00\n  nip:\n"), []string{`setup.yaml:6:3: start is no longer used. Remove it.`}},
		{"sessions as text", defaultWith(t, "sessions: 4", "sessions: four"), []string{`setup.yaml:7:15: nip.sessions four is not a whole number. Write digits only, such as 4.`}},
		{"widget tls in spec", defaultWith(t, "    brand_name: Beerus\n", "    brand_name: Beerus\n    widget_tls: true\n"), []string{`setup.yaml:13:5: unknown field "widget_tls". Remove it.`}},
		{"typo in spec", defaultWith(t, "  nip:", "  nap:"), []string{`setup.yaml:6:3: unknown field "nap" (did you mean "nip"?)`}},
		{"bad yaml", []byte("kind: [\n"), []string{"setup.yaml: read yaml: yaml: line 1: did not find expected node content"}},
		{"zero tsq attempts", defaultWith(t, "tsq_attempts: 3", "tsq_attempts: 0"), []string{`setup.yaml:9:5: nip.tsq_attempts 0 must be more than zero. Use a number such as 3, or leave it out.`}},
		{"zero cap", defaultWith(t, "      nuban_code: \"901\"\n", "      nuban_code: \"901\"\n      net_debit_cap_minor: 0\n"), []string{`setup.yaml:24:7: banks[bank-a].net_debit_cap_minor 0 must be more than zero. Give it in kobo, or leave it out for the default.`}},
		{"empty state", defaultWith(t, "      nuban_code: \"902\"\n", "      nuban_code: \"902\"\n      state: \"\"\n"), []string{`setup.yaml:29:7: banks[bank-b].state "" is not valid. Use active, suspended or closed.`}},
		{"unknown and bad type together", defaultWith(t, "type: digital", "type: online\n      colour: red"), []string{
			`setup.yaml:27:7: unknown field "colour". Remove it.`,
			`setup.yaml:26:7: banks[bank-b].type "online" is not a bank type. Use commercial, digital or microfinance.`,
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, errs := Parse("setup.yaml", c.raw)
			if got := errStrings(errs); !slices.Equal(got, c.want) {
				t.Fatalf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestParseAcceptsAliasesAndNullBlocks(t *testing.T) {
	raw := defaultWith(t, "  partners: []\n", "  partners:\n    - &p {id: balance, name: Balance}\n")
	raw = []byte(strings.Replace(string(raw), "  holidays:\n    add: []\n    remove: []\n", "  holidays:\n", 1))
	s, errs := Parse("setup.yaml", raw)
	if len(errs) > 0 {
		t.Fatal(errStrings(errs))
	}
	if len(s.Spec.Partners) != 1 || s.Spec.Partners[0].Name != "Balance" || s.Spec.Holidays.Add != nil {
		t.Fatalf("%+v", s.Spec)
	}
}

func TestParseDuplicateIDsPointAtTheSecond(t *testing.T) {
	raw := defaultWith(t, "  partners: []\n", "  partners:\n    - {id: balance, name: Balance}\n    - {id: balance, name: Other}\n")
	_, errs := Parse("setup.yaml", raw)
	want := []string{`setup.yaml:31:8: partners[1].id balance is already used by another partner. Give each partner its own id.`}
	if got := errStrings(errs); !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
}

func clearPos(s Spec) Spec {
	s.pos = nil
	return s
}

func TestExportRoundTrip(t *testing.T) {
	for _, name := range []string{"default.yaml", "three.yaml"} {
		t.Run(name, func(t *testing.T) {
			first := parseFixture(t, name)
			out, err := Export(first)
			if err != nil {
				t.Fatal(err)
			}
			second, errs := Parse("setup.yaml", out)
			if len(errs) > 0 {
				t.Fatalf("%v\n%s", errStrings(errs), out)
			}
			if !reflect.DeepEqual(clearPos(first), clearPos(second)) {
				t.Fatalf("round trip changed the spec:\n%+v\n%+v", first, second)
			}
			again, err := Export(second)
			if err != nil {
				t.Fatal(err)
			}
			if string(again) != string(out) {
				t.Fatalf("export is not stable:\n%s\n%s", out, again)
			}
		})
	}
}

func TestExportQuotesCodes(t *testing.T) {
	out, err := Export(parseFixture(t, "three.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{`      nip_code: "999001"`, `      nuban_code: "901"`, `      - "2026-12-28"`} {
		if !strings.Contains(string(out), line+"\n") {
			t.Errorf("export has no line %q:\n%s", line, out)
		}
	}
}
