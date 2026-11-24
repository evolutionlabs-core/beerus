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
	if p := three.Spec.Partners[0]; p.ID != "balance" || p.WebhookURL != "http://localhost:3000/webhooks" || len(p.RedirectURIs) != 1 {
		t.Fatalf("partner: %+v", p)
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

func TestParseErrors(t *testing.T) {
	two := append(readFixture(t, "default.yaml"), []byte("---\nkind: Setup\n")...)
	rows := []struct {
		name, file, old, new string
		raw                  []byte
		want                 []string
	}{
		{name: "bad name", file: "bad/bad-name.yaml", want: []string{`setup.yaml:4:3: metadata.name "Beerus_Setup" is not valid. Use 2 to 31 lowercase letters, digits or hyphens, starting with a letter.`}},
		{name: "closed without on_remove", file: "bad/closed-without-on-remove.yaml", want: []string{`setup.yaml:28:7: banks[bank-b].state closed needs on_remove. Add on_remove with money: transfer, payout or leave.`}},
		{name: "duplicate bank", file: "bad/duplicate-bank.yaml", want: []string{`setup.yaml:23:7: banks[1].id bank-a is already used by another bank. Give each bank its own id.`}},
		{name: "float amount", file: "bad/float-amount.yaml", want: []string{`setup.yaml:23:28: banks[bank-a].net_debit_cap_minor 1.5 is not a whole number. Give the amount in kobo, such as 150 for 1.50 naira.`}},
		{name: "remove to unknown", file: "bad/remove-to-unknown.yaml", want: []string{`setup.yaml:28:36: banks[bank-b].on_remove.to "bank-z" is not a bank in this file. Name a bank listed under banks.`}},
		{name: "two errors in one run", file: "bad/two-errors.yaml", want: []string{
			`setup.yaml:8:5: nip.tsq_after "soon" is not a duration. Use a duration such as 2m.`,
			`setup.yaml:25:7: banks[bank-b].type "online" is not a bank type. Use commercial, digital or microfinance.`,
		}},
		{name: "typo", file: "bad/unknown-field.yaml", want: []string{`setup.yaml:7:5: unknown field "sesions" (did you mean "sessions"?)`}},
		{name: "wrong version", file: "bad/wrong-version.yaml", want: []string{`setup.yaml:1:1: apiVersion "beerus.dev/v2" is not supported. Use beerus.dev/v1.`}},
		{name: "empty", raw: []byte(""), want: []string{`setup.yaml: the file is empty. Write the settings in it.`}},
		{name: "two documents", raw: two, want: []string{`setup.yaml:29:1: the file holds more than one document. Keep one setup per file.`}},
		{name: "scalar root", raw: []byte("hello\n"), want: []string{`setup.yaml:1:1: must be a mapping of fields. Write it as key: value lines.`}},
		{name: "bad yaml", raw: []byte("kind: [\n"), want: []string{"setup.yaml: read yaml: yaml: line 1: did not find expected node content"}},
		{name: "repeated key", old: "  name: beerus\n", new: "  name: beerus\n  name: other\n", want: []string{`setup.yaml:5:3: metadata.name appears twice. Keep one.`}},
		{name: "zero sessions", old: "sessions: 4", new: "sessions: 0", want: []string{`setup.yaml:7:5: nip.sessions 0 does not split the day into whole minutes. Use a number such as 1, 2, 4, 6, 12 or 24.`}},
		{name: "unknown field", old: "    brand_name: Beerus\n", new: "    brand_name: Beerus\n    widget_tls: true\n", want: []string{`setup.yaml:12:5: unknown field "widget_tls". Remove it.`}},
		{name: "zero cap", old: "      nuban_code: \"901\"\n", new: "      nuban_code: \"901\"\n      net_debit_cap_minor: 0\n", want: []string{`setup.yaml:23:7: banks[bank-a].net_debit_cap_minor 0 must be more than zero. Give it in kobo, or leave it out for the default.`}},
	}
	files, err := filepath.Glob(filepath.Join("testdata", "bad", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	covered := 0
	for _, r := range rows {
		raw := r.raw
		switch {
		case r.file != "":
			covered++
			raw = readFixture(t, r.file)
		case raw == nil:
			raw = defaultWith(t, r.old, r.new)
		}
		if _, errs := Parse("setup.yaml", raw); !slices.Equal(errStrings(errs), r.want) {
			t.Errorf("%s: got %q, want %q", r.name, errStrings(errs), r.want)
		}
	}
	if covered != len(files) {
		t.Errorf("testdata/bad has %d files, the table names %d", len(files), covered)
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

func clearPos(s Spec) Spec {
	s.pos = nil
	return s
}

func TestExport(t *testing.T) {
	for _, name := range []string{"default.yaml", "three.yaml"} {
		first := parseFixture(t, name)
		out, err := Export(first)
		if err != nil {
			t.Fatal(err)
		}
		second, errs := Parse("setup.yaml", out)
		if len(errs) > 0 {
			t.Fatalf("%s: %v\n%s", name, errStrings(errs), out)
		}
		if !reflect.DeepEqual(clearPos(first), clearPos(second)) {
			t.Fatalf("%s: round trip changed the spec:\n%+v\n%+v", name, first, second)
		}
		again, err := Export(second)
		if err != nil || string(again) != string(out) {
			t.Fatalf("%s: export is not stable: %v\n%s\n%s", name, err, out, again)
		}
		if name != "three.yaml" {
			continue
		}
		for _, line := range []string{`      nip_code: "999001"`, `      nuban_code: "901"`, `      - "2026-12-28"`} {
			if !strings.Contains(string(out), line+"\n") {
				t.Errorf("export has no line %q:\n%s", line, out)
			}
		}
	}
}
