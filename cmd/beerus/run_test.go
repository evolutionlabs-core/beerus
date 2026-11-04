package main

import (
	"strings"
	"testing"
)

func TestEnvNumberRejectsBadValues(t *testing.T) {
	t.Setenv("BEERUS_SEED", "")
	if n, err := envNumber("BEERUS_SEED", false); n != 0 || err != nil {
		t.Errorf("unset: %d %v", n, err)
	}
	t.Setenv("BEERUS_SEED", "-42")
	if n, err := envNumber("BEERUS_SEED", false); n != -42 || err != nil {
		t.Errorf("seed: %d %v", n, err)
	}
	for _, c := range []struct {
		value    string
		positive bool
	}{{"12a", false}, {"1.5", true}, {"0", true}, {"-3", true}} {
		t.Setenv("BEERUS_SEED", c.value)
		if _, err := envNumber("BEERUS_SEED", c.positive); err == nil || !strings.Contains(err.Error(), "BEERUS_SEED") || !strings.Contains(err.Error(), c.value) {
			t.Errorf("%q accepted or unnamed: %v", c.value, err)
		}
	}
}
