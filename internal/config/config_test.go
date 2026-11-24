package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func finish(c Config) (*Config, error) {
	c.Dir, c.TSQAfter = "x", 2*time.Minute
	return &c, c.Finish()
}

func TestFinishValidates(t *testing.T) {
	c := Config{Dir: "x"}
	if err := c.Finish(); err == nil || err.Error() != "nip status queries need a wait above zero, not 0s" {
		t.Errorf("finish with no wait: %v", err)
	}
	if c, err := finish(Config{}); err != nil || c.Sessions() != DefaultNIPSessions || c.APIPortOffset() != DefaultAPIPortOffset || c.PortWindow() != DefaultPortWindow {
		t.Fatalf("defaults: %v %v", c, err)
	}
	for _, ok := range []int{1, 2, 12, 24, 1440} {
		if _, err := finish(Config{NIPSessions: ok}); err != nil {
			t.Errorf("nip sessions %d: %v", ok, err)
		}
	}
	for _, bad := range []int{-1, 7, 11, 2880, 1000000000} {
		if _, err := finish(Config{NIPSessions: bad}); err == nil || !strings.Contains(err.Error(), "nip sessions") {
			t.Errorf("nip sessions %d: %v", bad, err)
		}
	}
	bank := func(a BankAPI) Config { return Config{Banks: []*Bank{{ID: "bank-a", API: a}}} }
	rng := "bank bank-a open_banking.access_seconds %d is out of range. Use 130 to 3600, or leave it out for the default"
	for _, c := range []struct {
		name string
		in   Config
		want string
	}{
		{"negative window", Config{Ports: Ports{Window: -1}}, "ports.window"},
		{"negative dormant_days", bank(BankAPI{DormantDays: -5}), "dormant_days"},
		{"low access_seconds", bank(BankAPI{AccessSeconds: 129}), fmt.Sprintf(rng, 129)},
		{"high access_seconds", bank(BankAPI{AccessSeconds: 3601}), "access_seconds 3601 is out of range"},
	} {
		if _, err := finish(c.in); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	if _, err := finish(bank(BankAPI{AccessSeconds: MaxAccessSeconds})); err != nil {
		t.Fatalf("access_seconds at the ceiling: %v", err)
	}
}

func TestPortAllocation(t *testing.T) {
	c, _ := finish(Config{})
	used := map[int]bool{8010: true, 8011: true}
	if p, err := c.AllocatePort(8010, used, func(p int) bool { return p != 8012 }); err != nil || p != 8013 {
		t.Fatalf("port %d: %v", p, err)
	}
	if _, err := c.AllocatePort(8010, used, func(int) bool { return false }); err == nil || !strings.Contains(err.Error(), "8010 to 8209") {
		t.Fatalf("no port free: %v", err)
	}
	c, err := finish(Config{Ports: Ports{APIOffset: 20, Window: 3}})
	if err != nil || c.APIPortOffset() != 20 {
		t.Fatalf("custom: %v", err)
	}
	if _, err := c.AllocatePort(9000, map[int]bool{9000: true, 9001: true, 9002: true}, func(int) bool { return true }); err == nil {
		t.Fatal("the window should stop the search")
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

func TestCarryOverSecret(t *testing.T) {
	dir := t.TempDir()
	partner := filepath.Join(dir, "partners", "myapp")
	secretPath := filepath.Join(partner, PartnerSecretFile)
	if err := os.MkdirAll(partner, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := CarryOverSecret(dir, "myapp"); err != nil {
		t.Fatalf("no old key: %v", err)
	}
	if _, err := os.Stat(secretPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a secret was made from nothing: %v", err)
	}
	if err := WriteSecret(filepath.Join(partner, "api-key"), "bk_test_old\n"); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		if err := CarryOverSecret(dir, "myapp"); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	got, err := ReadSecret(secretPath)
	if err != nil || got != "bk_test_old" {
		t.Fatalf("carried secret %q, %v, want bk_test_old", got, err)
	}
	info, err := os.Stat(secretPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("secret mode %v, %v, want 0600", info, err)
	}
	if err := WriteSecret(secretPath, "bk_test_new\n"); err != nil {
		t.Fatal(err)
	}
	if err := CarryOverSecret(dir, "myapp"); err != nil {
		t.Fatal(err)
	}
	if got, _ := ReadSecret(secretPath); got != "bk_test_new" {
		t.Fatalf("an existing secret was overwritten with %q", got)
	}
	if _, err := os.Stat(filepath.Join(partner, "api-key")); err != nil {
		t.Fatalf("the old key file was removed: %v", err)
	}
}
