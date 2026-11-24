package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type result struct {
	code int
	out  string
	err  string
}

func invoke(fn func(*cli, []string) error, args ...string) result {
	var out, errOut bytes.Buffer
	e := &cli{in: bufio.NewReader(strings.NewReader("")), out: &out, errOut: &errOut}
	code := e.finish(fn(e, args))
	return result{code, out.String(), errOut.String()}
}

func TestSetupCommandsAnswerWithoutARunningBeerus(t *testing.T) {
	dir := seeded(t)
	file := filepath.Join(dir, "copy.yaml")

	if r := invoke(cmdGet, "banks", "--dir", dir); r.code != 1 || r.err != "beerus: "+notRunning+"\n" {
		t.Errorf("get while stopped: %+v", r)
	}

	r := invoke(cmdExport, "--dir", dir, "-o", file)
	body, err := os.ReadFile(file)
	if r.code != 0 || r.out != "Wrote "+file+".\n" || err != nil || !strings.Contains(string(body), "bank-b") || strings.Contains(string(body), "nuban_code") {
		t.Fatalf("export should write the file without the assigned codes: %+v, %v\n%s", r, err, body)
	}
	if r := invoke(cmdDiff, "--dir", dir, "-f", file); r.code != 0 || r.out != "No changes.\n" {
		t.Fatalf("offline diff of the export: %+v", r)
	}
	renamed := filepath.Join(dir, "renamed.yaml")
	if err := os.WriteFile(renamed, []byte(strings.Replace(string(body), "name: Bank B", "name: Zenith Bank", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := invoke(cmdDiff, "--dir", dir, "-f", renamed); r.code != exitChanges || !strings.Contains(r.out, "~ bank bank-b") || !strings.Contains(r.out, "Zenith Bank") || r.err != "" {
		t.Fatalf("offline diff of a rename: %+v", r)
	}
	if r := invoke(cmdVerify, "--dir", dir); r.code != 0 || !regexp.MustCompile(`^setup sha256:[0-9a-f]{64}\n$`).MatchString(r.out) {
		t.Fatalf("verify: %+v", r)
	}

	for _, c := range []struct {
		fn   func(*cli, []string) error
		args []string
		want string
	}{
		{cmdExport, []string{"--dir", dir, "--json", "-o", file}, "beerus: --json and -o cannot be used together\n"},
		{cmdBankEnd, []string{"purge", "bank-a", "--dir", dir}, "beerus: purging bank-a deletes its database and cannot be undone. Nothing was changed. Run again with --yes to purge it\n"},
		{cmdBankEnd, []string{"purge", "nope", "--yes", "--dir", dir}, "beerus: there is no bank with ID nope. Run beerus get banks to list them\n"},
		{cmdBankEnd, []string{"close", "bank-a", "--to", "bank-b", "--dir", dir}, "beerus: --to and --customer need --money transfer, payout or leave\n"},
		{cmdBankEnd, []string{"purge", "bank-b", "--yes", "--wait", "0", "--dir", dir, "--money", "transfer"}, "beerus: --money transfer needs --to <bank>\n"},
		{cmdBankEnd, []string{"purge", "bank-b", "--yes", "--wait", "0", "--dir", dir, "--money", "move", "--to", "bank-a"}, "beerus: --money \"move\" is not valid. Use transfer, payout or leave\n"},
		{cmdApply, []string{"--wait", "-1s", "--dir", dir}, "beerus: --wait cannot be negative; use 0 to skip waiting\n"},
		{cmdBankEnd, []string{"close", "--dir", dir}, ""},
	} {
		r := invoke(c.fn, c.args...)
		if r.code != 1 || (c.want != "" && r.err != c.want) || (c.want == "" && !strings.Contains(r.err, "usage: beerus")) {
			t.Errorf("%v: %+v", c.args, r)
		}
	}
	if r := invoke(cmdDiff, "--dir", t.TempDir()); r.code != 1 || !strings.Contains(r.err, "holds no Beerus setup; run beerus init to make one") {
		t.Errorf("diff in an empty dir: %+v", r)
	}
}
