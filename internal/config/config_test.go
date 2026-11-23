package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func finish(c Config) (*Config, error) {
	c.Dir, c.TSQAfter = "x", 2*time.Minute
	return &c, c.Finish()
}

func TestStatusQueriesNeedAWait(t *testing.T) {
	c := Config{Dir: "x"}
	if err := c.Finish(); err == nil || err.Error() != "nip status queries need a wait above zero, not 0s" {
		t.Errorf("finish with no wait: %v", err)
	}
}

func TestNIPSessionsAreValidated(t *testing.T) {
	c, err := finish(Config{})
	if err != nil || c.Sessions() != DefaultNIPSessions {
		t.Fatalf("default: %v %v", c, err)
	}
	for _, ok := range []int{1, 2, 12, 24, 1440} {
		if _, err := finish(Config{NIPSessions: ok}); err != nil {
			t.Errorf("nip sessions %d: %v", ok, err)
		}
	}
	for _, bad := range []int{-1, 7, 11, 2880, 1000000000} {
		_, err := finish(Config{NIPSessions: bad})
		if err == nil || !strings.Contains(err.Error(), "nip sessions") {
			t.Errorf("nip sessions %d: %v", bad, err)
		}
	}
}

func TestPortAllocation(t *testing.T) {
	c, err := finish(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if c.APIPortOffset() != DefaultAPIPortOffset || c.PortWindow() != DefaultPortWindow {
		t.Fatalf("defaults %d %d", c.APIPortOffset(), c.PortWindow())
	}
	used := map[int]bool{8010: true, 8011: true}
	if p, err := c.AllocatePort(8010, used, func(p int) bool { return p != 8012 }); err != nil || p != 8013 {
		t.Fatalf("port %d: %v", p, err)
	}
	if _, err := c.AllocatePort(8010, used, func(int) bool { return false }); err == nil || !strings.Contains(err.Error(), "8010 to 8209") {
		t.Fatalf("no port free: %v", err)
	}
	c, err = finish(Config{Ports: Ports{APIOffset: 20, Window: 3}})
	if err != nil || c.APIPortOffset() != 20 {
		t.Fatalf("custom: %v", err)
	}
	if _, err := c.AllocatePort(9000, map[int]bool{9000: true, 9001: true, 9002: true}, func(int) bool { return true }); err == nil {
		t.Fatal("the window should stop the search")
	}
	if _, err := finish(Config{Ports: Ports{Window: -1}}); err == nil {
		t.Fatal("negative window accepted")
	}
	if _, err := finish(Config{Banks: []*Bank{{ID: "bank-a", API: BankAPI{DormantDays: -5}}}}); err == nil || !strings.Contains(err.Error(), "dormant_days") {
		t.Fatalf("negative dormant_days: %v", err)
	}
	want := "bank bank-a open_banking.access_seconds 129 is out of range. Use 130 to 3600, or leave it out for the default"
	if _, err := finish(Config{Banks: []*Bank{{ID: "bank-a", API: BankAPI{AccessSeconds: 129}}}}); err == nil || err.Error() != want {
		t.Fatalf("low access_seconds: %v", err)
	}
	if _, err := finish(Config{Banks: []*Bank{{ID: "bank-a", API: BankAPI{AccessSeconds: 3601}}}}); err == nil || !strings.Contains(err.Error(), "access_seconds 3601 is out of range") {
		t.Fatalf("high access_seconds: %v", err)
	}
	if _, err := finish(Config{Banks: []*Bank{{ID: "bank-a", API: BankAPI{AccessSeconds: MaxAccessSeconds}}}}); err != nil {
		t.Fatalf("access_seconds at the ceiling: %v", err)
	}
}

func TestPrepareIsIdempotentAndPlain(t *testing.T) {
	c, err := finish(Config{Connect: Connect{Host: "connect.localhost", PartnerPort: 7443, WidgetPort: 7444}, Banks: []*Bank{{ID: "bank-a", APIPort: 7451}}})
	if err != nil {
		t.Fatal(err)
	}
	c.Dir = t.TempDir()
	rows := []struct {
		name string
		got  string
		want string
	}{
		{"partner API", c.PartnerURL(), "http://connect.localhost:7443"},
		{"widget", c.WidgetURL(), "http://connect.localhost:7444"},
		{"bank API", c.Banks[0].APIURL(), "http://bank-a.localhost:7451"},
	}
	for _, r := range rows {
		if r.got != r.want {
			t.Errorf("%s URL = %q, want %q", r.name, r.got, r.want)
		}
	}
	files := []string{c.Path("connect", "ob-signing.key"), c.Path("webhook", "signing.key"), c.Path("control-token")}
	var first []string
	for i := 0; i < 2; i++ {
		if err := Prepare(c); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(f)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("%s: mode %v, error %v, want 0600", f, info, err)
			}
			got = append(got, string(b))
		}
		if i == 0 {
			first = got
			continue
		}
		for j := range got {
			if got[j] != first[j] {
				t.Errorf("a second prepare replaced %s", files[j])
			}
		}
	}
	if _, err := os.Stat(c.Path("ca")); !os.IsNotExist(err) {
		t.Fatalf("prepare made a ca directory: %v", err)
	}
}
