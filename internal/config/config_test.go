package config

import (
	"strings"
	"testing"
	"time"
)

func finish(c Config) (*Config, error) {
	c.Dir, c.TSQAfter, c.TSQAttempts = "x", 2*time.Minute, 3
	return &c, c.Finish()
}

func TestStatusQueriesNeedAWaitAndTries(t *testing.T) {
	for _, c := range []Config{{Dir: "x", TSQAttempts: 3}, {Dir: "x", TSQAfter: time.Minute}} {
		err := c.Finish()
		if err == nil || !strings.Contains(err.Error(), "nip status queries need a wait and a number of tries") {
			t.Errorf("%s and %d: %v", c.TSQAfter, c.TSQAttempts, err)
		}
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
}
