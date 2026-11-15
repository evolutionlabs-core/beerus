package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNIBSSFileEnsureAndLoad(t *testing.T) {
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	c := &Config{Dir: t.TempDir(), Connect: Connect{Host: "connect.localhost", WidgetPort: 9000, NIBSSPort: 9001, NIBSSControlPort: 9003, ControlPort: 9002, BrandName: "Connect"}, TSQAfter: 2 * time.Minute, TSQAttempts: 3}
	if err := Prepare(c, now); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadNIBSSFile(c.Dir); err == nil || err.Error() != "no NIBSS config at "+NIBSSFilePath(c.Dir)+"; start Beerus with beerus up first" {
		t.Fatalf("load before ensure: %v", err)
	}
	if err := c.EnsureNIBSSDir("wld_a", now); err != nil {
		t.Fatal(err)
	}
	dir := NIBSSDir(c.Dir)
	for name, want := range map[string]os.FileMode{NIBSSFileName: 0o600, NIBSSTokenFile: 0o600, NIBSSCAFile: 0o644, "server.key": 0o600, "client.key": 0o600} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil || fi.Mode().Perm() != want {
			t.Fatalf("%s: mode %v, error %v, want %v", name, fi, err, want)
		}
	}
	token, err := os.ReadFile(filepath.Join(dir, NIBSSTokenFile))
	if err != nil {
		t.Fatal(err)
	}
	c.Connect.NIBSSControlPort = 9004
	if err := c.EnsureNIBSSDir("wld_b", now); err != nil {
		t.Fatal(err)
	}
	if again, err := os.ReadFile(filepath.Join(dir, NIBSSTokenFile)); err != nil || string(again) != string(token) {
		t.Fatalf("the control token changed on a repeat: %v", err)
	}
	got, err := LoadNIBSSFile(c.Dir)
	if want := (NIBSSFile{Database: DatabaseName(c.Dir, NIBSSID), Port: 9001, ControlPort: 9004, WorldID: "wld_b", TSQAfter: "2m0s", TSQAttempts: 3}); err != nil || got != want {
		t.Fatalf("loaded %+v, %v, want %+v", got, err, want)
	}
	rows := []struct {
		name string
		body string
		err  string
	}{
		{"no ports", `{"database":"` + DatabaseName(c.Dir, NIBSSID) + `"}`, "port and control_port must be above zero"},
		{"same port twice", `{"database":"` + DatabaseName(c.Dir, NIBSSID) + `","port":9001,"control_port":9001}`, "port and control_port are both 9001; they must differ"},
		{"another database", `{"database":"beerus_other","port":9001,"control_port":9004,"tsq_after":"2m0s","tsq_attempts":3}`, `database is "beerus_other", but core names the NIBSS database "` + DatabaseName(c.Dir, NIBSSID) + `"`},
		{"no status query timing", `{"database":"` + DatabaseName(c.Dir, NIBSSID) + `","port":9001,"control_port":9004}`, `tsq_after and tsq_attempts need a duration above zero and a number above zero, not "" and 0`},
		{"not JSON", `{`, "unexpected end of JSON input"},
	}
	for _, r := range rows {
		if err := os.WriteFile(NIBSSFilePath(c.Dir), []byte(r.body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadNIBSSFile(c.Dir)
		want := NIBSSFilePath(c.Dir) + ": " + r.err
		if r.name == "not JSON" {
			want = "parse " + NIBSSFilePath(c.Dir) + ": " + r.err
		}
		if err == nil || err.Error() != want {
			t.Errorf("%s: got %v, want %q", r.name, err, want)
		}
	}
}
