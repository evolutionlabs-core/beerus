package main

import (
	"errors"
	"strings"
	"testing"
)

func helpOf(t *testing.T, args ...string) string {
	t.Helper()
	var h *helpError
	if err := dispatch(args); !errors.As(err, &h) {
		t.Fatalf("beerus %s returned %v, not help", strings.Join(args, " "), err)
	}
	return h.text
}

func TestHelp(t *testing.T) {
	top := helpOf(t)
	for _, args := range [][]string{{"help"}, {"--help"}} {
		if got := helpOf(t, args...); got != top {
			t.Errorf("beerus %v differs from beerus", args)
		}
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"scenario", "run"}, `unknown command "scenario". Run beerus help.`},
		{[]string{"help", "bank", "nope"}, `unknown command "bank nope". Run beerus help.`},
		{[]string{"bank", "create", "--bogus"}, "flag provided but not defined: -bogus; usage: beerus bank create --name NAME [options]"},
		{[]string{"bank", "nope"}, "usage: beerus bank list|create|suspend|resume|close|purge"},
	} {
		if err := dispatch(c.args); err == nil || err.Error() != c.want {
			t.Errorf("beerus %v: got %v, want %q", c.args, err, c.want)
		}
	}

	for _, c := range commands() {
		if c.summary == "" || strings.HasSuffix(c.summary, ".") {
			t.Errorf("%s has summary %q", c.path, c.summary)
		}
		if _, _, sub := strings.Cut(c.path, " "); !sub && !strings.Contains(top, "  "+c.path+" ") {
			t.Errorf("%s is missing from the top-level help", c.path)
		}
		text := helpOf(t, append(strings.Fields(c.path), "--help")...)
		if !strings.HasPrefix(text, "Usage: "+usageLine(c.path)+"\n") || text != helpOf(t, append([]string{"help"}, strings.Fields(c.path)...)...) {
			t.Errorf("beerus %s --help starts with %q, or differs from beerus help %s", c.path, text, c.path)
		}
	}
}

func TestCommandsWithoutArgumentsRejectOne(t *testing.T) {
	for _, command := range []string{"init", "version", "info", "doctor", "apply", "diff", "export", "verify"} {
		err := dispatch([]string{command, "extra", "--dir", t.TempDir()})
		want := `unexpected argument "extra"; ` + usageError(command).Error()
		if err == nil || err.Error() != want {
			t.Errorf("beerus %s extra: %v, want %q", command, err, want)
		}
	}
}
