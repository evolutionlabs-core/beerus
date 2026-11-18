package config

import "fmt"

const (
	DefaultAPIPortOffset = 10
	DefaultPortWindow    = 200
	MinAccessSeconds     = 130
	MaxAccessSeconds     = 3600
)

type Ports struct {
	APIOffset int `yaml:"api_offset,omitempty"`
	Window    int `yaml:"window,omitempty"`
}

type BankAPI struct {
	ConsentMonths    int `yaml:"consent_months,omitempty"`
	RefreshDays      int `yaml:"refresh_days,omitempty"`
	DormantDays      int `yaml:"dormant_days,omitempty"`
	AssertionSeconds int `yaml:"assertion_seconds,omitempty"`
	AccessSeconds    int `yaml:"access_seconds,omitempty"`
}

func accessSecondsError(seconds int) error {
	if seconds == 0 || (seconds >= MinAccessSeconds && seconds <= MaxAccessSeconds) {
		return nil
	}
	return fmt.Errorf("access_seconds %d is out of range. Use %d to %d, or leave it out for the default", seconds, MinAccessSeconds, MaxAccessSeconds)
}

func (c *Config) APIPortOffset() int {
	if c.Ports.APIOffset == 0 {
		return DefaultAPIPortOffset
	}
	return c.Ports.APIOffset
}

func (c *Config) PortWindow() int {
	if c.Ports.Window == 0 {
		return DefaultPortWindow
	}
	return c.Ports.Window
}

func (c *Config) AllocatePort(start int, used map[int]bool, free func(int) bool) (int, error) {
	end := min(start+c.PortWindow(), 65536)
	for p := start; p < end; p++ {
		if !used[p] && free(p) {
			return p, nil
		}
	}
	return 0, fmt.Errorf("no free port from %d to %d; free one or widen ports.window in local.yaml", start, end-1)
}

func (c *Config) validatePorts() error {
	if c.Ports.APIOffset < 0 || c.Ports.Window < 0 {
		return fmt.Errorf("ports.api_offset and ports.window cannot be negative")
	}
	for _, b := range c.Banks {
		for name, v := range map[string]int{"consent_months": b.API.ConsentMonths, "refresh_days": b.API.RefreshDays, "dormant_days": b.API.DormantDays, "assertion_seconds": b.API.AssertionSeconds, "access_seconds": b.API.AccessSeconds} {
			if v < 0 {
				return fmt.Errorf("bank %s open_banking.%s cannot be negative", b.ID, name)
			}
		}
		if err := accessSecondsError(b.API.AccessSeconds); err != nil {
			return fmt.Errorf("bank %s open_banking.%w", b.ID, err)
		}
	}
	return nil
}
