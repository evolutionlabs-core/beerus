package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/evolutionlabs-core/beerus/internal/clock"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	must(t, os.WriteFile(path, []byte(body), 0o600))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func private(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s: mode %v, error %v, want 0600", p, fi, err)
		}
	}
}

func rejects(t *testing.T, path string, load func() error, prefix string, rows map[string][2]string) {
	t.Helper()
	for name, r := range rows {
		write(t, path, r[0])
		if err := load(); err == nil || err.Error() != prefix+r[1] {
			t.Errorf("%s: got %v, want %q", name, err, prefix+r[1])
		}
	}
}

func TestEnsureAndLoadFiles(t *testing.T) {
	dir := t.TempDir()
	conn := Connect{Host: "connect.localhost", WidgetPort: 9000, NIBSSPort: 9001, NIBSSControlPort: 9003, CBNPort: 9005, CBNControlPort: 9006, ControlPort: 9002, BrandName: "Connect"}
	c := &Config{Dir: dir, SetupKey: 1<<63 + 5, Connect: conn, TSQAfter: 2 * time.Minute, NIPSessions: 6}
	must(t, Prepare(c))
	for _, f := range []struct {
		name, missing string
		load          func() error
	}{
		{"nibss", "no NIBSS config at " + NIBSSFilePath(dir) + "; start Beerus with beerus up first", func() error { _, err := LoadNIBSSFile(dir); return err }},
		{"cbn", "no CBN config at " + CBNFilePath(dir) + "; start Beerus with beerus up first", func() error { _, err := LoadCBNFile(dir); return err }},
	} {
		if err := f.load(); err == nil || err.Error() != f.missing {
			t.Fatalf("%s before ensure: %v", f.name, err)
		}
	}
	for _, f := range []struct {
		dir, file, token string
		ensure           func() error
	}{
		{NIBSSDir(dir), NIBSSFileName, NIBSSTokenFile, func() error { return c.EnsureNIBSSDir("wld_a") }},
		{CBNDir(dir), CBNFileName, CBNTokenFile, c.EnsureCBNDir},
	} {
		must(t, f.ensure())
		private(t, filepath.Join(f.dir, f.file), filepath.Join(f.dir, f.token))
	}
	nibssToken, cbnToken := readFile(t, filepath.Join(NIBSSDir(dir), NIBSSTokenFile)), readFile(t, filepath.Join(CBNDir(dir), CBNTokenFile))
	c.Connect.NIBSSControlPort, c.Connect.CBNControlPort = 9004, 9007
	must(t, c.EnsureNIBSSDir("wld_b"))
	must(t, c.EnsureCBNDir())
	if readFile(t, filepath.Join(NIBSSDir(dir), NIBSSTokenFile)) != nibssToken || readFile(t, filepath.Join(CBNDir(dir), CBNTokenFile)) != cbnToken {
		t.Fatal("a control token changed on a repeat")
	}
	nibss, err := LoadNIBSSFile(dir)
	if want := (NIBSSFile{Database: DatabaseName(dir, NIBSSID), Port: 9001, ControlPort: 9004, WorldID: "wld_b", TSQAfter: "2m0s", Sessions: 6, CBNPort: 9005, HolidaysAdd: []string{}, HolidaysRemove: []string{}}); err != nil || !reflect.DeepEqual(nibss, want) {
		t.Fatalf("nibss loaded %+v, %v, want %+v", nibss, err, want)
	}
	cbn, err := LoadCBNFile(dir)
	if want := (CBNFile{Database: DatabaseName(dir, CBNID), Port: 9005, ControlPort: 9007, WorldID: "wld_9223372036854775813"}); err != nil || cbn != want {
		t.Fatalf("cbn loaded %+v, %v, want %+v", cbn, err, want)
	}
	nd, cd := DatabaseName(dir, NIBSSID), DatabaseName(dir, CBNID)
	rejects(t, NIBSSFilePath(dir), func() error { _, err := LoadNIBSSFile(dir); return err }, NIBSSFilePath(dir)+": ", map[string][2]string{
		"no ports":         {`{"database":"` + nd + `"}`, "port and control_port must be above zero"},
		"same port twice":  {`{"database":"` + nd + `","port":9001,"control_port":9001}`, "port and control_port are both 9001; they must differ"},
		"another database": {`{"database":"beerus_other","port":9001,"control_port":9004,"tsq_after":"2m0s"}`, `database is "beerus_other", but core names the NIBSS database "` + nd + `"`},
		"no tsq":           {`{"database":"` + nd + `","port":9001,"control_port":9004}`, `tsq_after needs a duration above zero, not ""`},
		"no sessions":      {`{"database":"` + nd + `","port":9001,"control_port":9004,"tsq_after":"2m0s"}`, "sessions must be above zero, not 0"},
		"no CBN port":      {`{"database":"` + nd + `","port":9001,"control_port":9004,"tsq_after":"2m0s","sessions":6}`, "cbn_port must be above zero, not 0"},
	})
	write(t, NIBSSFilePath(dir), "{")
	if _, err := LoadNIBSSFile(dir); err == nil || err.Error() != "parse "+NIBSSFilePath(dir)+": unexpected end of JSON input" {
		t.Errorf("not JSON: %v", err)
	}
	rejects(t, CBNFilePath(dir), func() error { _, err := LoadCBNFile(dir); return err }, CBNFilePath(dir)+": ", map[string][2]string{
		"no ports":         {`{"database":"` + cd + `"}`, "port and control_port must be above zero"},
		"same port twice":  {`{"database":"` + cd + `","port":9005,"control_port":9005}`, "port and control_port are both 9005; they must differ"},
		"another database": {`{"database":"beerus_other","port":9005,"control_port":9007}`, `database is "beerus_other", but Beerus names the CBN database "` + cd + `"`},
	})
}

func TestBankFile(t *testing.T) {
	c := &Config{Dir: t.TempDir(), Connect: Connect{Host: "connect.localhost", WidgetPort: 9000, NIBSSPort: 9001, ControlPort: 9002, BrandName: "Connect"}, SetupKey: 1<<63 + 5}
	must(t, Prepare(c))
	b := &Bank{ID: "bank-a", Name: "Bank A", Type: "commercial", NIPCode: "000001", NUBANCode: "001", HistoryDays: 730, Port: 8001, APIPort: 8011, Database: DatabaseName(c.Dir, "bank-a"), NetDebitCap: 5_000_000, State: "suspended"}
	must(t, c.EnsureBankDir(b))
	dir := BankDir(c.Dir, "bank-a")
	path := filepath.Join(dir, BankFileName)
	private(t, path, filepath.Join(dir, BankTokenFile))
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	must(t, c.EnsureBankDir(b))
	if after, err := os.Stat(path); err != nil || !os.SameFile(before, after) {
		t.Fatalf("an unchanged bank file was written again: %v", err)
	}
	f, err := LoadBankFile(c.Dir, "bank-a")
	if got := f.Bank(); err != nil || *got != *b || f.State != "suspended" || f.CoreURL != "http://127.0.0.1:9002" || f.NIBSSPort != 9001 || f.BrandName != "Connect" || f.SetupKey != 1<<63+5 {
		t.Fatalf("loaded %+v and %+v, %v", f, got, err)
	}
	b.Name, b.State = "Bank Alpha", ""
	must(t, c.EnsureBankDir(b))
	if f, err = LoadBankFile(c.Dir, "bank-a"); err != nil || f.Name != "Bank Alpha" || f.State != "active" || f.Bank().State != "" {
		t.Fatalf("rename and resume: %+v, %v", f, err)
	}
	for _, id := range []string{"nosuch", "../x", ""} {
		want := `"` + id + `" is not a bank ID`
		if id == "nosuch" {
			want = "no bank config at " + BankFilePath(c.Dir, "nosuch") + "; create the bank with beerus bank create first"
		}
		if _, err := LoadBankFile(c.Dir, id); err == nil || err.Error() != want {
			t.Errorf("bank %q: %v, want %q", id, err, want)
		}
	}
	rangeMsg := path + ": open_banking.access_seconds %d is out of range. Use 130 to 3600, or leave it out for the default"
	for _, secs := range []int{129, 3601} {
		b.API.AccessSeconds = secs
		must(t, c.EnsureBankDir(b))
		if _, err := LoadBankFile(c.Dir, "bank-a"); err == nil || err.Error() != fmt.Sprintf(rangeMsg, secs) {
			t.Errorf("access seconds %d: %v", secs, err)
		}
	}
	b.API.AccessSeconds = MinAccessSeconds
	must(t, c.EnsureBankDir(b))
	if f, err = LoadBankFile(c.Dir, "bank-a"); err != nil || f.API.AccessSeconds != 130 {
		t.Fatalf("access seconds at the floor: %+v, %v", f.API, err)
	}
	db := DatabaseName(c.Dir, "bank-a")
	rejects(t, path, func() error { _, err := LoadBankFile(c.Dir, "bank-a"); return err }, path+": ", map[string][2]string{
		"another bank's file": {`{"id":"bank-b","port":1,"api_port":2,"database":"d","state":"active"}`, `id is "bank-b", want "bank-a"`},
		"no setup key":        {`{"id":"bank-a","port":1,"api_port":2,"database":"` + db + `","state":"active"}`, "setup_key is not set; start Beerus with beerus up to write it again"},
		"another database":    {`{"id":"bank-a","port":1,"api_port":2,"database":"d","state":"active"}`, `database is "d", but core names the database of bank-a "` + db + `"`},
	})
}

func TestHolidayOverrides(t *testing.T) {
	over, err := NIBSSFile{HolidaysAdd: []string{"2026-05-29"}, HolidaysRemove: []string{"2026-10-01"}}.Calendar()
	if err != nil {
		t.Fatal(err)
	}
	plain, err := NIBSSFile{}.Calendar()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		cal  clock.Calendar
		date clock.Date
		want bool
	}{
		{"added day", over, clock.Date{Y: 2026, M: 5, D: 29}, true},
		{"removed day", over, clock.Date{Y: 2026, M: 10, D: 1}, false},
		{"fixed day with overrides", over, clock.Date{Y: 2026, M: 12, D: 25}, true},
		{"fixed day without overrides", plain, clock.Date{Y: 2026, M: 10, D: 1}, true},
		{"ordinary day without overrides", plain, clock.Date{Y: 2026, M: 5, D: 29}, false},
	} {
		if got := c.cal.IsHoliday(c.date); got != c.want {
			t.Errorf("%s: IsHoliday = %t, want %t", c.name, got, c.want)
		}
	}
	for _, c := range []Config{{Holidays: &Holidays{Add: []string{"soon"}}}, {Holidays: &Holidays{Remove: []string{"soon"}}}} {
		if _, err := finish(c); err == nil {
			t.Error("a bad date should be rejected")
		}
	}
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
