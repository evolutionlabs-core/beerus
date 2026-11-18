package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCBNFileEnsureAndLoad(t *testing.T) {
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	c := &Config{Dir: t.TempDir(), SetupKey: 7, Connect: Connect{Host: "connect.localhost", WidgetPort: 9000, NIBSSPort: 9001, CBNPort: 9005, CBNControlPort: 9006, ControlPort: 9002, BrandName: "Connect"}}
	if err := Prepare(c, now); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCBNFile(c.Dir); err == nil || err.Error() != "no CBN config at "+CBNFilePath(c.Dir)+"; start Beerus with beerus up first" {
		t.Fatalf("load before ensure: %v", err)
	}
	if err := c.EnsureCBNDir(now); err != nil {
		t.Fatal(err)
	}
	dir := CBNDir(c.Dir)
	for name, want := range map[string]os.FileMode{CBNFileName: 0o600, CBNTokenFile: 0o600, CBNCAFile: 0o644, "server.key": 0o600} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil || fi.Mode().Perm() != want {
			t.Fatalf("%s: mode %v, error %v, want %v", name, fi, err, want)
		}
	}
	token, err := os.ReadFile(filepath.Join(dir, CBNTokenFile))
	if err != nil {
		t.Fatal(err)
	}
	c.Connect.CBNControlPort = 9007
	if err := c.EnsureCBNDir(now); err != nil {
		t.Fatal(err)
	}
	if again, err := os.ReadFile(filepath.Join(dir, CBNTokenFile)); err != nil || string(again) != string(token) {
		t.Fatalf("the control token changed on a repeat: %v", err)
	}
	got, err := LoadCBNFile(c.Dir)
	if want := (CBNFile{Database: DatabaseName(c.Dir, CBNID), Port: 9005, ControlPort: 9007, WorldID: "wld_7"}); err != nil || got != want {
		t.Fatalf("loaded %+v, %v, want %+v", got, err, want)
	}
	rows := []struct {
		name string
		body string
		err  string
	}{
		{"no ports", `{"database":"` + DatabaseName(c.Dir, CBNID) + `"}`, "port and control_port must be above zero"},
		{"same port twice", `{"database":"` + DatabaseName(c.Dir, CBNID) + `","port":9005,"control_port":9005}`, "port and control_port are both 9005; they must differ"},
		{"another database", `{"database":"beerus_other","port":9005,"control_port":9007}`, `database is "beerus_other", but Beerus names the CBN database "` + DatabaseName(c.Dir, CBNID) + `"`},
	}
	for _, r := range rows {
		if err := os.WriteFile(CBNFilePath(c.Dir), []byte(r.body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadCBNFile(c.Dir)
		if want := CBNFilePath(c.Dir) + ": " + r.err; err == nil || err.Error() != want {
			t.Errorf("%s: got %v, want %q", r.name, err, want)
		}
	}
}
