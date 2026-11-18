package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBankFileEnsureAndLoad(t *testing.T) {
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	c := &Config{Dir: t.TempDir(), Connect: Connect{Host: "connect.localhost", WidgetPort: 9000, NIBSSPort: 9001, ControlPort: 9002, BrandName: "Connect"}, SetupKey: 1<<63 + 5}
	if err := Prepare(c, now); err != nil {
		t.Fatal(err)
	}
	b := &Bank{ID: "bank-a", Name: "Bank A", Type: "commercial", NIPCode: "000001", NUBANCode: "001", HistoryDays: 730, Port: 8001, APIPort: 8011, Database: DatabaseName(c.Dir, "bank-a"), NetDebitCap: 5_000_000, State: "suspended"}
	if err := c.EnsureBankDir(b, now); err != nil {
		t.Fatal(err)
	}
	dir := BankDir(c.Dir, "bank-a")
	modes := map[string]os.FileMode{BankFileName: 0o600, BankTokenFile: 0o600, BankCAFile: 0o644, "client.key": 0o600, "server.key": 0o600, "events.key": 0o600}
	for name, want := range modes {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil || fi.Mode().Perm() != want {
			t.Fatalf("%s: mode %v, error %v, want %v", name, fi, err, want)
		}
	}
	path := filepath.Join(dir, BankFileName)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureBankDir(b, now); err != nil {
		t.Fatal(err)
	}
	if after, err := os.Stat(path); err != nil || !os.SameFile(before, after) {
		t.Fatalf("an unchanged bank file was written again: %v", err)
	}
	rows := []struct {
		name  string
		edit  func(*Bank)
		id    string
		check func(*testing.T, BankFile)
		err   string
	}{
		{name: "loaded as written", id: "bank-a", check: func(t *testing.T, f BankFile) {
			got := f.Bank()
			if *got != *b || f.NetDebitCapMinor != 5_000_000 || f.State != "suspended" || f.CoreURL != "http://127.0.0.1:9002" || f.NIBSSPort != 9001 || f.BrandName != "Connect" || f.SetupKey != 1<<63+5 {
				t.Fatalf("loaded %+v and %+v", f, got)
			}
		}},
		{name: "rename and resume", edit: func(b *Bank) { b.Name, b.State = "Bank Alpha", "" }, id: "bank-a", check: func(t *testing.T, f BankFile) {
			if f.Name != "Bank Alpha" || f.State != "active" || f.Bank().State != "" {
				t.Fatalf("loaded %+v", f)
			}
		}},
		{name: "no such bank", id: "nosuch", err: "no bank config at " + BankFilePath(c.Dir, "nosuch") + "; create the bank with beerus bank create first"},
		{name: "path in the ID", id: "../x", err: `"../x" is not a bank ID`},
		{name: "empty ID", id: "", err: `"" is not a bank ID`},
		{name: "access seconds too low", edit: func(b *Bank) { b.API.AccessSeconds = 129 }, id: "bank-a", err: BankFilePath(c.Dir, "bank-a") + ": open_banking.access_seconds 129 is out of range. Use 130 to 3600, or leave it out for the default"},
		{name: "access seconds too high", edit: func(b *Bank) { b.API.AccessSeconds = 3601 }, id: "bank-a", err: BankFilePath(c.Dir, "bank-a") + ": open_banking.access_seconds 3601 is out of range. Use 130 to 3600, or leave it out for the default"},
		{name: "access seconds at the floor", edit: func(b *Bank) { b.API.AccessSeconds = MinAccessSeconds }, id: "bank-a", check: func(t *testing.T, f BankFile) {
			if f.API.AccessSeconds != 130 {
				t.Fatalf("loaded %+v", f.API)
			}
		}},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			if r.edit != nil {
				r.edit(b)
				if err := c.EnsureBankDir(b, now); err != nil {
					t.Fatal(err)
				}
			}
			f, err := LoadBankFile(c.Dir, r.id)
			if r.err != "" {
				if err == nil || err.Error() != r.err {
					t.Fatalf("got error %v, want %q", err, r.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			r.check(t, f)
		})
	}
	if err := os.WriteFile(path, []byte(`{"id":"bank-b","port":1,"api_port":2,"database":"d","state":"active"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBankFile(c.Dir, "bank-a"); err == nil || err.Error() != path+`: id is "bank-b", want "bank-a"` {
		t.Fatalf("another bank's file: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"id":"bank-a","port":1,"api_port":2,"database":"d","state":"active"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"id":"bank-a","port":1,"api_port":2,"database":"`+DatabaseName(c.Dir, "bank-a")+`","state":"active"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBankFile(c.Dir, "bank-a"); err == nil || err.Error() != path+": setup_key is not set; start Beerus with beerus up to write it again" {
		t.Fatalf("no setup key: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"id":"bank-a","port":1,"api_port":2,"database":"d","state":"active"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	want := path + `: database is "d", but core names the database of bank-a "` + DatabaseName(c.Dir, "bank-a") + `"`
	if _, err := LoadBankFile(c.Dir, "bank-a"); err == nil || err.Error() != want {
		t.Fatalf("another database: %v, want %s", err, want)
	}
}
