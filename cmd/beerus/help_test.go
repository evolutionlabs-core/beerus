package main

import (
	"errors"
	"strings"
	"testing"
)

const wantTopHelp = `Usage: beerus <command> [args]

Beerus runs Nigeria's banking infrastructure on your machine:
banks, NIBSS, the CBN and open banking, in one binary.

Main commands:
  up            Start Beerus and apply your setup
  bank          Create, suspend, close and remove banks
  customer      Create and close customers
  account       Open and close accounts
  transfer      Send money between accounts over NIP
  nip           Check NIP sessions and settle now

Setup:
  init          Create the state directory and a setup file
  apply         Apply the setup file to the running Beerus
  diff          Show what apply would change
  get           List banks or partners
  describe      Show one bank or partner in detail
  export        Write the running setup as a file
  verify        Print the hash of the applied setup
  reset         Drop the databases and clear the state directory

Banking:
  use           Choose the bank that later commands use
  customers     List a bank's customers
  accounts      List a bank's accounts
  deposit       Put cash into an account
  withdraw      Take cash out of an account
  statement     Show an account's statement

Open banking:
  partner       Add, list, rekey and remove partner apps
  registry      Open banking clients and consents
  connections   List partner connections
  revoke        Revoke a connection at the bank
  sync          Sync all connections now
  webhooks      List webhook deliveries and replay one

Failures and recovery:
  fault         Make a bank go down or time out
  snapshot      Save and restore Beerus's data

Running Beerus:
  run           Run one participant: cbn, core or a bank
  compose       Print a Docker Compose file for the setup
  info          Show addresses and partner apps
  doctor        Find and clean up leftovers
  trust         Trust the Beerus root certificate
  untrust       Stop trusting the Beerus root certificate
  health        Check that a URL answers
  version       Print the version

Global options:
  -h, --help    Show help for any command

Environment:
  BEERUS_DIR    State directory (default .beerus)

Run "beerus <command> --help" for a command's options.
`

const wantBankCreateHelp = `Usage: beerus bank create --name NAME [options]

Create a bank.

Options:
  --id ID                  Bank ID, derived from the name when empty
  --name NAME              Bank name
  --nip-code CODE          6-digit NIP institution code, assigned when empty
  --nuban-code CODE        3-digit NUBAN bank code, assigned when empty
  --settles-through BANK   Bank that a microfinance bank settles through
  --type TYPE              Commercial, digital or microfinance (default commercial)
`

func helpOf(t *testing.T, args ...string) string {
	t.Helper()
	var h *helpError
	if err := dispatch(args); !errors.As(err, &h) {
		t.Fatalf("beerus %s returned %v, not help", strings.Join(args, " "), err)
	}
	return h.text
}

func TestHelp(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
		fail bool
	}{
		{nil, wantTopHelp, false},
		{[]string{"help"}, wantTopHelp, false},
		{[]string{"help", "--help"}, wantTopHelp, false},
		{[]string{"--help"}, wantTopHelp, false},
		{[]string{"-h"}, wantTopHelp, false},
		{[]string{"bank", "create", "--help"}, wantBankCreateHelp, false},
		{[]string{"bank", "create", "-h"}, wantBankCreateHelp, false},
		{[]string{"help", "bank", "create"}, wantBankCreateHelp, false},
		{[]string{"nope"}, `unknown command "nope". Run beerus help.`, true},
		{[]string{"clock"}, `unknown command "clock". Run beerus help.`, true},
		{[]string{"scenarios"}, `unknown command "scenarios". Run beerus help.`, true},
		{[]string{"scenario", "run"}, `unknown command "scenario". Run beerus help.`, true},
		{[]string{"help", "nope"}, `unknown command "nope". Run beerus help.`, true},
		{[]string{"help", "bank", "nope"}, `unknown command "bank nope". Run beerus help.`, true},
		{[]string{"bank", "create", "--bogus"}, "flag provided but not defined: -bogus; usage: beerus bank create --name NAME [options]", true},
		{[]string{"bank", "nope"}, "usage: beerus bank list|create|suspend|resume|close|purge", true},
		{[]string{"customer"}, "usage: beerus customer create|close", true},
	} {
		if c.fail {
			if err := dispatch(c.args); err == nil || err.Error() != c.want {
				t.Errorf("beerus %v: got %v, want %q", c.args, err, c.want)
			}
			continue
		}
		if got := helpOf(t, c.args...); got != c.want {
			t.Errorf("beerus %v:\n got %q\nwant %q", c.args, got, c.want)
		}
	}

	groups := map[string]bool{}
	for _, g := range groupOrder() {
		groups[g] = true
	}
	seen := map[string]bool{}
	for _, c := range commands() {
		if seen[c.path] {
			t.Errorf("%s is listed twice", c.path)
		}
		seen[c.path] = true
		if c.summary == "" || strings.HasSuffix(c.summary, ".") {
			t.Errorf("%s has summary %q", c.path, c.summary)
		}
		parent, _, isSub := strings.Cut(c.path, " ")
		switch {
		case isSub && (c.group != "" || c.run != nil):
			t.Errorf("%s is a subcommand and must not have a group or run", c.path)
		case isSub && !seen[parent]:
			t.Errorf("%s comes before its command %s", c.path, parent)
		case !isSub && (!groups[c.group] || c.run == nil):
			t.Errorf("%s needs a known group and a run function", c.path)
		}
		if c.group != "" && !strings.Contains(wantTopHelp, "  "+c.path+" ") {
			t.Errorf("%s is missing from the top-level help", c.path)
		}
		text := helpOf(t, append(strings.Fields(c.path), "--help")...)
		if !strings.HasPrefix(text, "Usage: "+usageLine(c.path)+"\n") {
			t.Errorf("beerus %s --help starts with %q", c.path, text)
		}
		if text != helpOf(t, append([]string{"help"}, strings.Fields(c.path)...)...) {
			t.Errorf("beerus help %s differs from beerus %s --help", c.path, c.path)
		}
	}
}
